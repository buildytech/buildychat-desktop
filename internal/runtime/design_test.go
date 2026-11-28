package runtime

// Design mode (ADR 0070): the chat contract binds cases/design.json to the web hosts only, so the desktop runs the
// same cases here (the shared runner, whatever the case's surfaces) plus what only the desktop has: the OpenRouter
// provider, the approved list, the method and a key that must not come back.

import (
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	buildychat "github.com/buildytech/buildychat-desktop"
	"io"
	"path/filepath"
	"sync"
)

func TestDesignContractCases(t *testing.T) {
	contract := buildychat.Contract()
	constants, err := LoadConstants(contract)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := fs.ReadFile(contract, "cases/design.json")
	if err != nil {
		t.Skipf("no design cases in the pinned contract: %v", err)
	}
	var file caseFile
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatal(err)
	}
	if len(file.Cases) < 4 {
		t.Fatalf("only %d design cases", len(file.Cases))
	}
	for _, c := range file.Cases {
		t.Run(c.Name, func(t *testing.T) {
			for _, p := range runCase(t, contract, constants, c) {
				t.Error(p)
			}
		})
	}
}

func designRequest(t *testing.T, srv *Server, method, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(method, "wails://localhost/runtime/v1/design", strings.NewReader(body))
	srv.Middleware(http.NotFoundHandler()).ServeHTTP(rec, req)
	return rec
}

func TestDesignIsNotA404(t *testing.T) {
	constants, err := LoadConstants(buildychat.Contract())
	if err != nil {
		t.Fatal(err)
	}
	srv := New(Options{Constants: constants, Keys: fixedKey("")})
	body := `{"modelId":"` + constants.CandidateChatModel + `","messages":[{"role":"user","content":"A bakery brand"}]}`
	if rec := designRequest(t, srv, http.MethodPost, body); rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "xai_api_key_missing") {
		t.Fatalf("no key: %d %s", rec.Code, rec.Body.String())
	}
	if rec := designRequest(t, srv, http.MethodGet, ""); rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET: %d", rec.Code)
	}
	if rec := designRequest(t, srv, http.MethodPost, "{"); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad json: %d", rec.Code)
	}
	if rec := designRequest(t, srv, http.MethodPost, `{"modelId":"m","messages":[{"role":"tool","content":"x"}]}`); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("bad role: %d", rec.Code)
	}
}

type designWire struct {
	mu      sync.Mutex
	models  int
	chats   int
	lastRaw []byte
	reply   string
}

func (d *designWire) RoundTrip(r *http.Request) (*http.Response, error) {
	path := r.URL.Path
	if r.Method == http.MethodGet && (strings.HasSuffix(path, "/models") || strings.Contains(path, "/images/models") || strings.HasSuffix(path, "/language-models") || strings.HasSuffix(path, "/image-generation-models")) {
		d.mu.Lock()
		d.models++
		d.mu.Unlock()
		body := `{"data":[{"id":"x-ai/grok-4.20"},{"id":"x-ai/grok-4.7"},{"id":"grok-4.20-0309-non-reasoning"},{"id":"grok-4.7"}]}`
		if strings.Contains(path, "image") {
			body = `{"data":[{"id":"x-ai/grok-imagine-image-2.0"},{"id":"grok-imagine-image-2.0"}]}`
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{"Content-Type": []string{"application/json"}}, Request: r}, nil
	}
	raw, _ := io.ReadAll(r.Body)
	d.mu.Lock()
	d.chats++
	d.lastRaw = raw
	reply := d.reply
	d.mu.Unlock()
	if reply == "" {
		reply = `{"choices":[{"message":{"content":"{\"brief\":{\"name\":\"Rye\"}}"}}],"usage":{"cost":0.001}}`
	}
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(reply)), Header: http.Header{"Content-Type": []string{"application/json"}}, Request: r}, nil
}

func designServer(t *testing.T, wire http.RoundTripper, xai, openrouter string) *Server {
	t.Helper()
	constants, err := LoadConstants(buildychat.Contract())
	if err != nil {
		t.Fatal(err)
	}
	var or KeyStore
	if openrouter != "" {
		or = &memoryKeys{key: openrouter}
	}
	var keys KeySource
	if xai != "" {
		keys = fixedKey(xai)
	} else {
		keys = fixedKey("")
	}
	return New(Options{Constants: constants, Keys: keys, BaseURL: testBase, Client: &http.Client{Transport: wire}, OpenRouter: or, OpenRouterBaseURL: testORBase, SettingsFile: filepath.Join(t.TempDir(), "providers.json")})
}

func TestDesignOpenRouterMapsAppModelId(t *testing.T) {
	// v0.1.0 compared the app id to OpenRouter's list and answered model_not_listed; the UI then showed a generic
	// "The design request could not be completed." The catalog loop already maps grok-4.20-0309-non-reasoning.
	wire := &designWire{}
	srv := designServer(t, wire, testKey, testORKey)
	body := `{"modelId":"grok-4.20-0309-non-reasoning","messages":[{"role":"user","content":"A bakery brand"}],"provider":"openrouter"}`
	rec := designRequest(t, srv, http.MethodPost, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d %s", rec.Code, rec.Body.String())
	}
	if wire.chats != 1 {
		t.Fatalf("chats %d", wire.chats)
	}
	var sent struct {
		Model               string                  `json:"model"`
		Reasoning           *struct{ Enabled bool } `json:"reasoning"`
		MaxCompletionTokens *int                    `json:"max_completion_tokens"`
		MaxTokens           *int                    `json:"max_tokens"`
		Stream              *bool                   `json:"stream"`
	}
	if err := json.Unmarshal(wire.lastRaw, &sent); err != nil {
		t.Fatal(err)
	}
	if sent.Model != "x-ai/grok-4.20" {
		t.Fatalf("model %q", sent.Model)
	}
	if sent.Reasoning == nil || sent.Reasoning.Enabled {
		t.Fatalf("reasoning %+v", sent.Reasoning)
	}
	if sent.MaxCompletionTokens == nil || *sent.MaxCompletionTokens != maxDesignTokens {
		t.Fatalf("max_completion_tokens %+v", sent.MaxCompletionTokens)
	}
	if sent.MaxTokens != nil {
		t.Fatalf("unexpected max_tokens")
	}
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out["ok"] != true {
		t.Fatalf("body %s", rec.Body.String())
	}
	if _, ok := out["costUsdTicks"]; !ok {
		t.Fatalf("missing costUsdTicks in %s", rec.Body.String())
	}
}

func TestDesignNamedXAIWhenSignedInWithOpenRouter(t *testing.T) {
	wire := &designWire{}
	srv := designServer(t, wire, testKey, testORKey)
	body := `{"modelId":"grok-4.20-0309-non-reasoning","messages":[{"role":"user","content":"A bakery brand"}],"provider":"xai"}`
	rec := designRequest(t, srv, http.MethodPost, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d %s", rec.Code, rec.Body.String())
	}
	var sent struct {
		Model     string          `json:"model"`
		Reasoning json.RawMessage `json:"reasoning"`
	}
	if err := json.Unmarshal(wire.lastRaw, &sent); err != nil {
		t.Fatal(err)
	}
	if sent.Model != "grok-4.20-0309-non-reasoning" {
		t.Fatalf("xAI must receive the app id, got %q", sent.Model)
	}
	if len(sent.Reasoning) > 0 && string(sent.Reasoning) != "null" {
		t.Fatalf("xAI must not get a reasoning switch: %s", sent.Reasoning)
	}
}

func TestDesignOpenRouterUnknownAppModelIsListedError(t *testing.T) {
	wire := &designWire{}
	srv := designServer(t, wire, testKey, testORKey)
	body := `{"modelId":"grok-9","messages":[{"role":"user","content":"A bakery brand"}],"provider":"openrouter"}`
	rec := designRequest(t, srv, http.MethodPost, body)
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "model_not_listed") {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if wire.chats != 0 {
		t.Fatalf("provider was called")
	}
}
