// Package webapp serves the built apps/chat-web site (SvelteKit adapter-static): a file when it exists, else the SPA
// fallback 200.html, the way vercel.json rewrites every other path to /200.html.
package webapp

import (
	"io/fs"
	"net/http"
	"path"
	"strings"
)

const fallback = "200.html"

// Handler serves site; when site has no 200.html it answers a short page that says how to build the frontend.
func Handler(site fs.FS) http.Handler {
	files := http.FileServerFS(site)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
		if name != "" && name != fallback {
			if info, err := fs.Stat(site, name); err == nil && !info.IsDir() {
				if strings.HasPrefix(name, "_app/immutable/") {
					w.Header().Set("cache-control", "public, max-age=31536000, immutable")
				}
				files.ServeHTTP(w, r)
				return
			}
		}
		page, err := fs.ReadFile(site, fallback)
		w.Header().Set("cache-control", "no-cache")
		w.Header().Set("content-type", "text/html; charset=utf-8")
		if err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte("<!doctype html><title>BuildY</title><p>The web app is not built into this binary. Run scripts/build-frontend.sh, then build again.</p>"))
			return
		}
		_, _ = w.Write(page)
	})
}
