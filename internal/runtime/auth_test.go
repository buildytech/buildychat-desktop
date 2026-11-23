package runtime

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	buildychat "github.com/buildytech/buildychat-desktop"
)

// exchangeStub answers POST <base>/auth/keys with a key when the verifier matches the challenge the browser got.
type exchangeStub struct{ challenge string }

func (e *exchangeStub) RoundTrip(r *http.Request) (*http.Response, error) {
	var body map[string]string
	raw, _ := io.ReadAll(r.Body)
	_ = json.Unmarshal(raw, &body)
	status, answer := 403, `{"error":{"message":"Invalid code"}}`
	if r.URL.String() == testORBase+"/auth/keys" && body["code"] == "c-live" && challengeOf(body["code_verifier"]) == e.challenge && body["code_challenge_method"] == "S256" {
		status, answer = 200, `{"key":"`+testORKey+`"}`
	}
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(answer)), Request: r}, nil
}

// The whole sign-in over a real loopback port: start, the browser's callback on 127.0.0.1, the key kept, the listener
// closed afterwards, a second callback refused.
func TestSignInOverLoopback(t *testing.T) {
	constants, err := LoadConstants(buildychat.Contract())
	if err != nil {
		t.Fatal(err)
	}
	stub := &exchangeStub{}
	keys := &memoryKeys{}
	var opened string
	srv := New(Options{Constants: constants, Keys: fixedKey(""), Client: &http.Client{Transport: stub}, OpenRouter: keys, OpenRouterBaseURL: testORBase,
		OpenURL: func(u string) error { opened = u; return nil }})
	defer srv.Close()
	rec := httptest.NewRecorder()
	srv.Middleware(http.NotFoundHandler()).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "wails://localhost/runtime/v1/auth/openrouter/start", strings.NewReader("{}")))
	if rec.Code != 200 || opened == "" || strings.Contains(rec.Body.String(), "verifier") {
		t.Fatalf("start: %d %s", rec.Code, rec.Body.String())
	}
	auth, _ := url.Parse(opened)
	stub.challenge = auth.Query().Get("code_challenge")
	callback, _ := url.Parse(auth.Query().Get("callback_url"))
	if callback.Hostname() != "localhost" || !strings.HasPrefix(callback.Path, "/cb/") {
		t.Fatalf("callback %s", callback)
	}
	direct := "http://127.0.0.1:" + callback.Port() + callback.Path
	res, err := http.Get(direct + "?code=c-live")
	if err != nil {
		t.Fatal(err)
	}
	page, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != 200 || strings.Contains(string(page), testORKey) {
		t.Fatalf("callback: %d", res.StatusCode)
	}
	if key, _ := keys.Key(); key != testORKey {
		t.Fatal("the key was not kept")
	}
	if srv.signInPending() {
		t.Fatal("the sign-in is still pending")
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if res, err := http.Get(direct + "?code=c-live"); err != nil {
			return // the listener is closed
		} else {
			res.Body.Close()
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("the loopback listener stayed open after the callback")
}
