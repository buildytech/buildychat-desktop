package runtime

// POST /runtime/v1/design (ADR 0070 in buildytech/chat, apps/xai-runtime/src/design.ts): one Design-agent chat
// completion with no tools, answered as { ok, text }. The host keeps the key; the web app validates the proposal and
// writes the Brand Skin. The provider is the one the app is signed in with (OpenRouter when it holds a key, else xAI),
// so Design works on the desktop the way it does on the web hosts; the owner's approved chat list narrows the model.

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	designPath       = "/v1/design"
	maxDesignBody    = 128_000
	maxDesignMsgs    = 40
	maxDesignContent = 16_000
	maxDesignTokens  = 8192
	designTimeout    = 60 * time.Second
	maxDesignAnswer  = 4 << 20
)

func designJSON(w http.ResponseWriter, status int, body map[string]any) {
	writeJSON(w, status, body)
}

func designFail(w http.ResponseWriter, status int, code, message string) {
	designJSON(w, status, map[string]any{"ok": false, "code": code, "message": message})
}

type designMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// designMessages reads the list the way design.ts does: 1..40 rows of system|user|assistant with a text of up to 16000
// characters (`content`, else `text`); nil when it is not one.
func designMessages(v any) []designMessage {
	rows, ok := v.([]any)
	if !ok || len(rows) == 0 || len(rows) > maxDesignMsgs {
		return nil
	}
	out := make([]designMessage, 0, len(rows))
	for _, row := range rows {
		rec, ok := row.(map[string]any)
		if !ok {
			return nil
		}
		role, _ := rec["role"].(string)
		content, has := rec["content"].(string)
		if !has {
			content, has = rec["text"].(string)
		}
		if (role != "system" && role != "user" && role != "assistant") || !has || jsLength(content) > maxDesignContent {
			return nil
		}
		out = append(out, designMessage{Role: role, Content: content})
	}
	return out
}

// designText reads choices[0].message.content: a string, or the joined text parts.
func designText(payload any) (string, bool) {
	rec, ok := payload.(map[string]any)
	if !ok {
		return "", false
	}
	choices, ok := rec["choices"].([]any)
	if !ok || len(choices) == 0 {
		return "", false
	}
	choice, ok := choices[0].(map[string]any)
	if !ok {
		return "", false
	}
	msg, ok := choice["message"].(map[string]any)
	if !ok {
		return "", false
	}
	switch c := msg["content"].(type) {
	case string:
		return c, true
	case []any:
		var parts []string
		for _, p := range c {
			if m, ok := p.(map[string]any); ok {
				if t, ok := m["text"].(string); ok {
					parts = append(parts, t)
				}
			}
		}
		if len(parts) > 0 {
			return strings.Join(parts, ""), true
		}
	}
	return "", false
}

func (s *Server) design(w http.ResponseWriter, r *http.Request, path, method string) {
	if path != designPath {
		designFail(w, http.StatusNotFound, "not_found", "Unknown runtime path.")
		return
	}
	if method != http.MethodPost {
		w.Header().Set("allow", "POST")
		designFail(w, http.StatusMethodNotAllowed, "method_not_allowed", "Design answers POST only.")
		return
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, maxDesignBody+1))
	if err != nil {
		designFail(w, http.StatusBadRequest, "invalid_json", "invalid json")
		return
	}
	if len(raw) > maxDesignBody {
		designFail(w, http.StatusRequestEntityTooLarge, "too_large", "payload too large")
		return
	}
	var body any
	if len(bytes.TrimSpace(raw)) == 0 {
		body = map[string]any{}
	} else if json.Unmarshal(raw, &body) != nil {
		designFail(w, http.StatusBadRequest, "invalid_json", "invalid json")
		return
	}
	rec, ok := body.(map[string]any)
	if !ok {
		designFail(w, http.StatusBadRequest, "invalid_body", "Send one JSON object.")
		return
	}
	messages := designMessages(rec["messages"])
	if messages == nil {
		designFail(w, http.StatusUnprocessableEntity, "invalid_request", "messages is a non-empty list of role and content.")
		return
	}
	modelID, _ := rec["modelId"].(string)
	if strings.TrimSpace(modelID) == "" {
		designFail(w, http.StatusUnprocessableEntity, "missing_model_id", "Select a Grok chat model before sending.")
		return
	}
	// The Design role's provider (ADR 0071): the app sends "xai" or "openrouter". Omitted keeps the v0.1.0 default
	// (OpenRouter when signed in, else xAI). The app's model ids are mapped on the OpenRouter wire.
	provider, _ := rec["provider"].(string)
	provider = strings.TrimSpace(provider)
	if provider != "" && provider != "xai" && provider != "openrouter" {
		designFail(w, http.StatusUnprocessableEntity, "invalid_request", "provider is xai or openrouter.")
		return
	}
	xaiKey := ""
	if s.keys != nil {
		xaiKey, _ = s.keys.Key()
	}
	orKey := s.orKey()
	if provider == "" {
		if orKey != "" {
			provider = "openrouter"
		} else {
			provider = "xai"
		}
	}
	t := target{id: "xai", name: "xAI", url: s.baseURL() + "/chat/completions", key: xaiKey}
	if provider == "openrouter" {
		t = target{id: "openrouter", name: "OpenRouter", url: s.orBaseURL() + "/chat/completions", key: orKey, extra: openRouterAttribution}
	}
	if t.key == "" {
		code, name := "xai_api_key_missing", "xAI"
		if t.id == "openrouter" {
			code, name = "openrouter_key_missing", "OpenRouter"
		}
		designFail(w, http.StatusServiceUnavailable, code, "No "+name+" key is stored on this computer. No provider call was made.")
		return
	}
	sendID := strings.TrimSpace(modelID)
	var reasoning any
	if t.id == "openrouter" {
		route, ok := openRouterRoute(modelID)
		if !ok {
			designFail(w, http.StatusUnprocessableEntity, "model_not_listed", "This key does not list that chat model. No chat request was sent.")
			return
		}
		sendID = route.id
		if route.reasoning != nil {
			reasoning = map[string]any{"enabled": *route.reasoning}
		}
	}
	if list, narrowed := s.approvedList(t.id, "chat"); narrowed && !contains(list, modelID) && !contains(list, sendID) {
		designFail(w, http.StatusForbidden, "model_not_approved", "That chat model is not approved in Settings. No chat request was sent.")
		return
	}
	var ids []string
	var code string
	if t.id == "openrouter" {
		ids, code = s.orModels.get(t.key, func() discovered { return s.orDiscover(r.Context(), t.key) })
	} else {
		ids, code = s.models.get(t.key, func() discovered { return s.discover(r.Context(), t.key) })
	}
	if code != "" {
		designFail(w, http.StatusServiceUnavailable, code, "The models of this key could not be listed. No chat request was sent.")
		return
	}
	if !contains(ids, sendID) {
		designFail(w, http.StatusUnprocessableEntity, "model_not_listed", "This key does not list that chat model. No chat request was sent.")
		return
	}
	bodyOut := map[string]any{"model": sendID, "messages": messages, "stream": false, "max_completion_tokens": maxDesignTokens, "temperature": 0}
	if reasoning != nil {
		bodyOut["reasoning"] = reasoning
	}
	payload, _ := json.Marshal(bodyOut)
	started := time.Now()
	logDesign := func(ok bool, code string, status int) {
		log.Printf(`{"runtime":"xai-grok","event":"design","provider":%q,"ok":%t,"code":%q,"status":%d,"ms":%d}`, t.name, ok, code, status, time.Since(started).Milliseconds())
	}
	failed := func(code string) {
		designFail(w, http.StatusBadGateway, code, "The design request could not be completed. No automatic retry was sent.")
	}
	ctx, cancel := context.WithTimeout(r.Context(), designTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.url, bytes.NewReader(payload))
	if err != nil {
		logDesign(false, "design_failed", 0)
		failed("design_failed")
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+t.key)
	for name, v := range t.extra {
		req.Header.Set(name, v)
	}
	res, err := s.client().Do(req)
	if err != nil {
		logDesign(false, "design_failed", 0)
		failed("design_failed")
		return
	}
	defer res.Body.Close()
	answer, err := io.ReadAll(io.LimitReader(res.Body, maxDesignAnswer+1))
	if err == nil && len(answer) <= maxDesignAnswer {
		s.noteProvider(t.id, res.StatusCode, answer)
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		logDesign(false, "provider_error", res.StatusCode)
		failed("provider_error")
		return
	}
	var doc any
	if err != nil || len(answer) > maxDesignAnswer || json.Unmarshal(answer, &doc) != nil {
		logDesign(false, "malformed_response", res.StatusCode)
		designFail(w, http.StatusBadGateway, "malformed_response", "The provider answer had no assistant text.")
		return
	}
	text, ok := designText(doc)
	if !ok {
		logDesign(false, "malformed_response", res.StatusCode)
		designFail(w, http.StatusBadGateway, "malformed_response", "The provider answer had no assistant text.")
		return
	}
	text = strings.ReplaceAll(text, t.key, "[redacted]")
	logDesign(true, "ok", res.StatusCode)
	out := map[string]any{"ok": true, "text": text}
	if ticks := designTicks(doc, s.constants.OpenRouter.USDTicks); ticks != nil {
		out["costUsdTicks"] = ticks
	}
	designJSON(w, http.StatusOK, out)
}

// designTicks reads a billed amount: xAI usage.cost_in_usd_ticks, or OpenRouter usage.cost in USD converted with
// constants.openrouter.usdTicks. Missing cost is omitted, never zero.
func designTicks(payload any, usdTicks float64) any {
	rec, ok := payload.(map[string]any)
	if !ok {
		return nil
	}
	usage, ok := rec["usage"].(map[string]any)
	if !ok {
		return nil
	}
	switch v := usage["cost_in_usd_ticks"].(type) {
	case float64:
		if v >= 0 && v == math.Trunc(v) {
			return int64(v)
		}
	case json.Number:
		if n, err := v.Int64(); err == nil && n >= 0 {
			return n
		}
	case string:
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n >= 0 {
			return n
		}
	}
	if usd, ok := usage["cost"].(float64); ok && usd >= 0 && usdTicks > 0 {
		n := math.Round(usd * usdTicks)
		if n >= 0 && n == math.Trunc(n) {
			return int64(n)
		}
	}
	return nil
}
