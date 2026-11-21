package runtime

// The provider forward of the agent loop that runs in the webview (capabilities.loop "client", ADR 0065, contract
// cases/xai-forward.json): POST /v1/xai/chat/completions only. The webview sends a placeholder key; the forward puts
// the key it holds on the request, accepts only a chat model the key lists, caps the body, follows no redirect and
// redacts the key from whatever comes back. It logs nothing but the status and the time.

import (
	"bytes"
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
	maxForwardBody   = 256_000
	maxForwardAnswer = 16 << 20
	modelsTTL        = 5 * time.Minute
)

// The headers of the webview's request that reach the provider, and of the provider's answer that reach the webview.
var (
	forwardRequestHeaders = []string{"Accept", "Content-Type", "X-Grok-Conv-Id"}
	forwardAnswerHeaders  = []string{"Content-Type", "Retry-After"}
)

type modelCache struct {
	mu      sync.Mutex
	key     string
	chatIDs []string
	at      time.Time
}

// chatModels are the chat ids the key lists (discovery, cached per key for a few minutes).
func (s *Server) chatModels(r *http.Request, key string) ([]string, string) {
	s.models.mu.Lock()
	if s.models.key == key && time.Since(s.models.at) < modelsTTL && len(s.models.chatIDs) > 0 {
		ids := s.models.chatIDs
		s.models.mu.Unlock()
		return ids, ""
	}
	s.models.mu.Unlock()
	d := s.discover(r.Context(), key)
	if !d.ok {
		return nil, d.code
	}
	s.models.mu.Lock()
	s.models.key, s.models.chatIDs, s.models.at = key, d.chatIDs, time.Now()
	s.models.mu.Unlock()
	return d.chatIDs, ""
}

func forwardError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": message}})
}

func (s *Server) forward(w http.ResponseWriter, r *http.Request, path, method string) {
	if path != forwardPath {
		forwardError(w, http.StatusNotFound, "not_found", "Unknown runtime path.")
		return
	}
	if method != http.MethodPost {
		w.Header().Set("allow", "POST")
		forwardError(w, http.StatusMethodNotAllowed, "method_not_allowed", "The forward answers POST only.")
		return
	}
	key := ""
	if s.keys != nil {
		key, _ = s.keys.Key()
	}
	if key == "" {
		forwardError(w, http.StatusServiceUnavailable, "xai_api_key_missing", "No xAI key is stored on this computer. No provider call was made.")
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
	ids, code := s.chatModels(r, key)
	if code != "" {
		forwardError(w, http.StatusServiceUnavailable, code, "The models of this key could not be listed. No chat request was sent.")
		return
	}
	if !contains(ids, *body.Model) {
		forwardError(w, http.StatusUnprocessableEntity, "model_not_listed", "This key does not list that chat model. No chat request was sent.")
		return
	}
	started := time.Now()
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, s.baseURL()+"/chat/completions", bytes.NewReader(raw))
	if err != nil {
		forwardError(w, http.StatusBadGateway, "xai_forward_failed", "The xAI chat request could not be sent.")
		return
	}
	for _, name := range forwardRequestHeaders {
		if v := r.Header.Get(name); v != "" {
			req.Header.Set(name, v)
		}
	}
	if req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Authorization", "Bearer "+key)
	res, err := s.client().Do(req)
	if err != nil {
		log.Printf(`{"runtime":"xai-grok","event":"xai_forward","ok":false,"code":"xai_forward_failed","ms":%d}`, time.Since(started).Milliseconds())
		forwardError(w, http.StatusBadGateway, "xai_forward_failed", "The xAI chat request could not be completed.")
		return
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 && res.StatusCode < 400 {
		log.Printf(`{"runtime":"xai-grok","event":"xai_forward","ok":false,"code":"xai_forward_failed","status":%d,"ms":%d}`, res.StatusCode, time.Since(started).Milliseconds())
		forwardError(w, http.StatusBadGateway, "xai_forward_failed", "The xAI chat request was redirected; it was not followed.")
		return
	}
	answer, err := io.ReadAll(io.LimitReader(res.Body, maxForwardAnswer+1))
	if err != nil || len(answer) > maxForwardAnswer {
		forwardError(w, http.StatusBadGateway, "xai_forward_failed", "The xAI chat answer could not be read.")
		return
	}
	answer = bytes.ReplaceAll(answer, []byte(key), []byte("[redacted]"))
	for _, name := range forwardAnswerHeaders {
		if v := res.Header.Get(name); v != "" {
			w.Header().Set(name, strings.ReplaceAll(v, key, "[redacted]"))
		}
	}
	w.Header().Set("cache-control", "no-store")
	w.WriteHeader(res.StatusCode)
	_, _ = w.Write(answer)
	log.Printf(`{"runtime":"xai-grok","event":"xai_forward","ok":%t,"status":%d,"ms":%d}`, res.StatusCode < 300, res.StatusCode, time.Since(started).Milliseconds())
}
