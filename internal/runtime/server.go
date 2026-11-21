// Package runtime answers the BuildY runtime HTTP contract (buildytech/chat contract/runtime-http, ADR 0065) inside the
// desktop app: the web app's fetch("/runtime/v1/...") reaches it through the Wails asset-server middleware, in process,
// on the app's own origin. The xAI key stays here; no answer carries it.
package runtime

import (
	"encoding/json"
	"net/http"
	"strings"
)

// KeySource yields the xAI key and where it came from ("keychain" or "env"); "" when none is set.
type KeySource interface {
	Key() (key string, source string)
}

// Capabilities is the optional `capabilities` of /v1/status (contract/runtime-http/capabilities.schema.json).
type Capabilities struct {
	Loop     string `json:"loop"`
	Files    bool   `json:"files"`
	SaveFile bool   `json:"saveFile"`
}

// Server is the runtime of one desktop app.
type Server struct {
	constants    Constants
	keys         KeySource
	base         string
	httpClient   *http.Client
	capabilities Capabilities
}

// Options configure a Server. BaseURL defaults to the contract's xAI base URL; Client to a client without redirects.
type Options struct {
	Constants    Constants
	Keys         KeySource
	BaseURL      string
	Client       *http.Client
	Capabilities *Capabilities
}

// DesktopCapabilities is what this build implements: the agent turn runs in the webview (phase 2 adds the provider
// forward); local picture files and the native save come with phases 3 and 4.
var DesktopCapabilities = Capabilities{Loop: "client", Files: false, SaveFile: false}

// New builds the runtime.
func New(o Options) *Server {
	s := &Server{constants: o.Constants, keys: o.Keys, base: strings.TrimRight(strings.TrimSpace(o.BaseURL), "/"), httpClient: o.Client, capabilities: DesktopCapabilities}
	if o.Capabilities != nil {
		s.capabilities = *o.Capabilities
	}
	return s
}

func (s *Server) baseURL() string {
	if s.base != "" {
		return s.base
	}
	return s.constants.XAI.DefaultBaseURL
}

func (s *Server) client() *http.Client {
	if s.httpClient != nil {
		return s.httpClient
	}
	return &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

// stripRuntimePrefix mirrors packages/chat-agent/src/protect.ts.
func stripRuntimePrefix(path string) string {
	if path == "" {
		path = "/"
	}
	for _, prefix := range []string{"/api/runtime", "/runtime", "/api"} {
		if path == prefix {
			return "/"
		}
		if strings.HasPrefix(path, prefix+"/") {
			path = path[len(prefix):]
			break
		}
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return path
}

// IsRuntimePath tells the middleware which requests are the runtime's (the rest is the web app).
func IsRuntimePath(path string) bool {
	for _, prefix := range []string{"/runtime", "/api"} {
		if path == prefix || strings.HasPrefix(path, prefix+"/") {
			return true
		}
	}
	return false
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("content-type", "application/json")
	w.Header().Set("cache-control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// ServeHTTP routes one runtime request (apps/xai-runtime/src/http.ts handleRuntimeFetch).
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := stripRuntimePrefix(r.URL.Path)
	method := strings.ToUpper(r.Method)
	w.Header().Set("x-content-type-options", "nosniff")
	if method == http.MethodOptions {
		w.Header().Set("cache-control", "no-store")
		w.WriteHeader(http.StatusNoContent)
		return
	}
	switch {
	case method == http.MethodGet && (path == "/healthz" || path == "/health"):
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "runtime": s.constants.Runtime})
	case method == http.MethodGet && (path == "/v1/status" || path == "/v1/runtime"):
		doc := s.status(r.Context())
		status := http.StatusServiceUnavailable
		if doc.Key.Present {
			status = http.StatusOK
		}
		writeJSON(w, status, doc)
	default:
		writeJSON(w, http.StatusNotFound, map[string]any{"error": map[string]string{"code": "not_found", "message": "Unknown runtime path."}})
	}
}

// Middleware puts the runtime in front of the web app (Wails AssetOptions.Middleware).
func (s *Server) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if IsRuntimePath(r.URL.Path) {
			s.ServeHTTP(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}
