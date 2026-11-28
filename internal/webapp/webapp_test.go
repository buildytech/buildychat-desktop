package webapp

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func TestFilesThenFallback(t *testing.T) {
	site := fstest.MapFS{
		"200.html":            {Data: []byte("<!doctype html><p>app</p>")},
		"_app/immutable/a.js": {Data: []byte("export const a = 1;")},
		"render/blocks.mjs":   {Data: []byte("export const blocks = {};")},
	}
	h := Handler(site)
	cases := []struct{ path, want, typ string }{
		{"/", "app", "text/html"},
		{"/editor/page-1", "app", "text/html"},
		// the standalone preview of a page (the web app's previewWindowUrl), with its query: the app shell, not a 404
		{"/home?preview&session=session-b066e122-c354-492d-9464-d3c3cc4b95e1", "app", "text/html"},
		{"/preview?session=session-b066e122-c354-492d-9464-d3c3cc4b95e1", "app", "text/html"},
		{"/_app/immutable/a.js", "export const a", "javascript"},
		{"/render/blocks.mjs", "export const blocks", "javascript"},
	}
	for _, c := range cases {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "wails://localhost"+c.path, nil))
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), c.want) || !strings.Contains(rec.Header().Get("content-type"), c.typ) {
			t.Errorf("%s: %d %q %q", c.path, rec.Code, rec.Header().Get("content-type"), rec.Body.String())
		}
	}
}

func TestNotBuilt(t *testing.T) {
	rec := httptest.NewRecorder()
	Handler(fstest.MapFS{}).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "wails://localhost/", nil))
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "build-frontend.sh") {
		t.Errorf("got %d %q", rec.Code, rec.Body.String())
	}
}
