package runtime

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/buildytech/buildychat-desktop"
)

const ownerSession = "session-b066e122-c354-492d-9464-d3c3cc4b95e1"

func TestPreviewTargetAcceptsOnlyAnInAppPreviewAddress(t *testing.T) {
	ok := []string{
		"/home?preview&session=" + ownerSession, // the owner's address (the page slug is "home")
		"/preview?session=" + ownerSession,
		"/quiet-rooms?preview&session=s1",
	}
	for _, raw := range ok {
		if path, name, good := previewTarget(raw); !good || path != raw || !strings.HasPrefix(name, "preview-") {
			t.Errorf("%q: got %q %q %v", raw, path, name, good)
		}
	}
	_, a, _ := previewTarget(ok[0])
	_, b, _ := previewTarget("/other?preview&session=" + ownerSession)
	if a != b {
		t.Errorf("one window per session: %q vs %q", a, b)
	}
	bad := []string{
		"", "home?preview&session=s1", "//evil.example/home?preview&session=s1", "http://wails.localhost/home?preview&session=s1",
		"https://evil.example/preview?session=s1", "/home?session=s1", "/home?preview", "/home?preview&session=",
		"/home?preview&session=a b", "/home?preview&session=../x", "/runtime/v1/status?preview&session=s1", "/api/runtime/v1/export?preview&session=s1",
		"/a/../runtime/v1/status?preview&session=s1", "/home?preview&session=s1#x", "\\\\host\\x?preview&session=s1", "/" + strings.Repeat("a", 500) + "?preview&session=s1",
	}
	for _, raw := range bad {
		if _, _, good := previewTarget(raw); good {
			t.Errorf("%q must be refused", raw)
		}
	}
}

func previewServer(t *testing.T, open PreviewWindow) *Server {
	t.Helper()
	constants, err := LoadConstants(buildychat.Contract())
	if err != nil {
		t.Fatal(err)
	}
	return New(Options{Constants: constants, Keys: fixedKey(""), OpenWindow: open})
}

func previewRequest(srv *Server, method, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	srv.Middleware(http.NotFoundHandler()).ServeHTTP(rec, httptest.NewRequest(method, "wails://localhost/runtime/v1/preview-window", strings.NewReader(body)))
	return rec
}

func TestPreviewWindowOpensTheOwnersAddress(t *testing.T) {
	var gotName, gotPath string
	srv := previewServer(t, func(name, path string) error { gotName, gotPath = name, path; return nil })
	rec := previewRequest(srv, http.MethodPost, `{"path":"/home?preview&session=`+ownerSession+`"}`)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"ok":true`) {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
	if gotPath != "/home?preview&session="+ownerSession || !strings.HasPrefix(gotName, "preview-") {
		t.Fatalf("window %q at %q", gotName, gotPath)
	}
}

func TestPreviewWindowRefusesWhatIsNotAPreviewAddress(t *testing.T) {
	opened := false
	srv := previewServer(t, func(string, string) error { opened = true; return nil })
	for _, body := range []string{`{"path":"https://example.com/"}`, `{"path":"/runtime/v1/status?preview&session=s1"}`, `{"path":"/home"}`, `{}`} {
		rec := previewRequest(srv, http.MethodPost, body)
		if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "invalid_path") {
			t.Errorf("%s: %d %s", body, rec.Code, rec.Body.String())
		}
	}
	if rec := previewRequest(srv, http.MethodPost, `not json`); rec.Code != http.StatusBadRequest {
		t.Errorf("bad json: %d", rec.Code)
	}
	if rec := previewRequest(srv, http.MethodGet, ``); rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET: %d", rec.Code)
	}
	if opened {
		t.Fatal("a window was opened for a refused address")
	}
}

func TestPreviewWindowWithoutAWindowSaysUnsupportedSoTheBrowserOpensIt(t *testing.T) {
	rec := previewRequest(previewServer(t, nil), http.MethodPost, `{"path":"/home?preview&session=`+ownerSession+`"}`)
	if rec.Code != http.StatusNotImplemented || !strings.Contains(rec.Body.String(), "unsupported") {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
}

func TestPreviewWindowFailureIsReported(t *testing.T) {
	srv := previewServer(t, func(string, string) error { return ErrCancelled })
	rec := previewRequest(srv, http.MethodPost, `{"path":"/preview?session=s1"}`)
	if rec.Code != http.StatusBadGateway || !strings.Contains(rec.Body.String(), "open_failed") {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
}
