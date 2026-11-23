package runtime

// The provider forwards of the agent loop that runs in the webview (capabilities.loop "client", ADR 0065 and 0067,
// contract cases/xai-forward.json and cases/openrouter-forward.json): POST /v1/xai/chat/completions with the xAI key and
// POST /v1/openrouter/chat/completions with the user's OpenRouter key. The webview sends a placeholder key; the forward
// puts the key it holds on the request, accepts only a chat model that key lists (OpenRouter: only the contract's x-ai
// models), caps the body, follows no redirect and redacts the key from whatever comes back. It logs nothing but the
// status and the time.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"
)

const (
	forwardPath      = "/v1/xai/chat/completions"
	forwardPrefix    = "/v1/xai/"
	orForwardPath    = "/v1/openrouter/chat/completions"
	orForwardPrefix  = "/v1/openrouter/"
	maxForwardBody   = 256_000
	maxForwardAnswer = 16 << 20
	modelsTTL        = 5 * time.Minute
)

// The headers of the provider's answer that reach the webview.
var forwardAnswerHeaders = []string{"Content-Type", "Retry-After"}

type modelCache struct {
	mu      sync.Mutex
	key     string
	chatIDs []string
	at      time.Time
}

// get answers the cached chat ids of key, else discovers them (cached per key for a few minutes).
func (m *modelCache) get(key string, discover func() discovered) ([]string, string) {
	m.mu.Lock()
	if m.key == key && time.Since(m.at) < modelsTTL && len(m.chatIDs) > 0 {
		ids := m.chatIDs
		m.mu.Unlock()
		return ids, ""
	}
	m.mu.Unlock()
	d := discover()
	if !d.ok {
		return nil, d.code
	}
	m.mu.Lock()
	m.key, m.chatIDs, m.at = key, d.chatIDs, time.Now()
	m.mu.Unlock()
	return d.chatIDs, ""
}

func (m *modelCache) clear() {
	m.mu.Lock()
	m.key, m.chatIDs = "", nil
	m.mu.Unlock()
}

func forwardError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": message}})
}

// target is one provider's forward.
type target struct {
	name        string // in messages and logs
	path        string
	url         string
	key         string
	missingCode string
	failCode    string
	// headers maps the webview's request headers to the provider's names.
	headers map[string]string
	// extra headers the host adds (attribution).
	extra  map[string]string
	models func(ctx context.Context) ([]string, string)
}

func (s *Server) forward(w http.ResponseWriter, r *http.Request, path, method string) {
	key := ""
	if s.keys != nil {
		key, _ = s.keys.Key()
	}
	s.forwardTo(w, r, path, method, target{
		name: "xAI", path: forwardPath, url: s.baseURL() + "/chat/completions", key: key,
		missingCode: "xai_api_key_missing", failCode: "xai_forward_failed",
		headers: map[string]string{"Accept": "Accept", "Content-Type": "Content-Type", "X-Grok-Conv-Id": "X-Grok-Conv-Id"},
		models: func(ctx context.Context) ([]string, string) {
			return s.models.get(key, func() discovered { return s.discover(ctx, key) })
		},
	})
}

func (s *Server) orForward(w http.ResponseWriter, r *http.Request, path, method string) {
	key := s.orKey()
	s.forwardTo(w, r, path, method, target{
		name: "OpenRouter", path: orForwardPath, url: s.orBaseURL() + "/chat/completions", key: key,
		missingCode: "openrouter_key_missing", failCode: "openrouter_forward_failed",
		headers: map[string]string{"Accept": "Accept", "Content-Type": "Content-Type", "X-Grok-Conv-Id": s.constants.OpenRouter.SessionHeader},
		extra:   openRouterAttribution,
		models: func(ctx context.Context) ([]string, string) {
			return s.orModels.get(key, func() discovered { return s.orDiscover(ctx, key) })
		},
	})
}

func (s *Server) forwardTo(w http.ResponseWriter, r *http.Request, path, method string, t target) {
	if path != t.path {
		forwardError(w, http.StatusNotFound, "not_found", "Unknown runtime path.")
		return
	}
	if method != http.MethodPost {
		w.Header().Set("allow", "POST")
		forwardError(w, http.StatusMethodNotAllowed, "method_not_allowed", "The forward answers POST only.")
		return
	}
	if t.key == "" {
		forwardError(w, http.StatusServiceUnavailable, t.missingCode, "No "+t.name+" key is stored on this computer. No provider call was made.")
		return
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxForwardBody+1))
	if err != nil {
		forwardError(w, http.StatusBadRequest, "invalid_json", "invalid json")
		return
	}
	if len(raw) > maxForwardBody {
		forwardError(w, http.StatusRequestEntityTooLarge, "too_large", "payload too large")
		return
	}
	var body struct {
		Model *string `json:"model"`
	}
	if json.Unmarshal(raw, &body) != nil {
		forwardError(w, http.StatusBadRequest, "invalid_json", "invalid json")
		return
	}
	if body.Model == nil || strings.TrimSpace(*body.Model) == "" {
		forwardError(w, http.StatusBadRequest, "invalid_request", "The request names no model.")
		return
	}
	ids, code := t.models(r.Context())
	if code != "" {
		forwardError(w, http.StatusServiceUnavailable, code, "The models of this key could not be listed. No chat request was sent.")
		return
	}
	if !contains(ids, *body.Model) {
		forwardError(w, http.StatusUnprocessableEntity, "model_not_listed", "This key does not list that chat model. No chat request was sent.")
		return
	}
	started := time.Now()
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, t.url, bytes.NewReader(raw))
	if err != nil {
		forwardError(w, http.StatusBadGateway, t.failCode, "The "+t.name+" chat request could not be sent.")
		return
	}
	for from, to := range t.headers {
		if v := r.Header.Get(from); v != "" {
			req.Header.Set(to, v)
		}
	}
	for name, v := range t.extra {
		req.Header.Set(name, v)
	}
	if req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+t.key)
	res, err := s.client().Do(req)
	if err != nil {
		log.Printf(`{"runtime":"xai-grok","event":"forward","provider":%q,"ok":false,"code":%q,"ms":%d}`, t.name, t.failCode, time.Since(started).Milliseconds())
		forwardError(w, http.StatusBadGateway, t.failCode, "The "+t.name+" chat request could not be completed.")
		return
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 && res.StatusCode < 400 {
		log.Printf(`{"runtime":"xai-grok","event":"forward","provider":%q,"ok":false,"code":%q,"status":%d,"ms":%d}`, t.name, t.failCode, res.StatusCode, time.Since(started).Milliseconds())
		forwardError(w, http.StatusBadGateway, t.failCode, "The "+t.name+" chat request was redirected; it was not followed.")
		return
	}
	answer, err := io.ReadAll(io.LimitReader(res.Body, maxForwardAnswer+1))
	if err != nil || len(answer) > maxForwardAnswer {
		forwardError(w, http.StatusBadGateway, t.failCode, "The "+t.name+" chat answer could not be read.")
		return
	}
	answer = bytes.ReplaceAll(answer, []byte(t.key), []byte("[redacted]"))
	for _, name := range forwardAnswerHeaders {
		if v := res.Header.Get(name); v != "" {
			w.Header().Set(name, strings.ReplaceAll(v, t.key, "[redacted]"))
		}
	}
	w.Header().Set("cache-control", "no-store")
	w.WriteHeader(res.StatusCode)
	_, _ = w.Write(answer)
	log.Printf(`{"runtime":"xai-grok","event":"forward","provider":%q,"ok":%t,"status":%d,"ms":%d}`, t.name, res.StatusCode < 300, res.StatusCode, time.Since(started).Milliseconds())
}
