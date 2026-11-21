package runtime

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	buildychat "github.com/buildytech/buildychat-desktop"
)

type countingProvider struct {
	listings, chats atomic.Int32
	auth            atomic.Value
}

func (p *countingProvider) RoundTrip(r *http.Request) (*http.Response, error) {
	body := `{"data":[{"id":"grok-4.20-0309-non-reasoning"}]}`
	if strings.HasSuffix(r.URL.Path, "/chat/completions") {
		p.chats.Add(1)
		p.auth.Store(r.Header.Get("Authorization"))
		body = `{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`
	} else {
		p.listings.Add(1)
		if strings.HasSuffix(r.URL.Path, "/image-generation-models") {
			body = `{"data":[]}`
		}
	}
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
}

// The key's model list is read once per key and reused by the next turns' requests (a turn makes up to ten).
func TestForwardReusesTheModelList(t *testing.T) {
	constants, err := LoadConstants(buildychat.Contract())
	if err != nil {
		t.Fatal(err)
	}
	p := &countingProvider{}
	srv := New(Options{Constants: constants, Keys: fixedKey(testKey), BaseURL: testBase, Client: &http.Client{Transport: p}})
	for i := 0; i < 3; i++ {
		req := httptest.NewRequest(http.MethodPost, "wails://localhost/runtime/v1/xai/chat/completions", strings.NewReader(`{"model":"grok-4.20-0309-non-reasoning","messages":[]}`))
		req.Header.Set("Authorization", "Bearer host-held")
		rec := httptest.NewRecorder()
		srv.Middleware(http.NotFoundHandler()).ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("turn %d: %d %s", i, rec.Code, rec.Body.String())
		}
	}
	if got := p.listings.Load(); got != int32(len(constants.XAI.Discovery)) {
		t.Errorf("model list read %d times, want one discovery (%d requests)", got, len(constants.XAI.Discovery))
	}
	if p.chats.Load() != 3 || p.auth.Load() != "Bearer "+testKey {
		t.Errorf("chats %d, auth replaced: %v", p.chats.Load(), p.auth.Load() == "Bearer "+testKey)
	}
}
