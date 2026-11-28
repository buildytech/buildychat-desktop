// Package runtime answers the BuildY runtime HTTP contract (buildytech/chat contract/runtime-http, ADR 0065) inside the
// desktop app: the web app's fetch("/runtime/v1/...") reaches it through the Wails asset-server middleware, in process,
// on the app's own origin. The xAI key stays here; no answer carries it.
package runtime

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"regexp"
	goruntime "runtime"
	"strings"
	"sync"

	"github.com/buildytech/buildychat-desktop/internal/media"
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
	// SignIn lists the sign-ins the host offers (ADR 0067): "openrouter" when it can keep an OpenRouter key.
	SignIn []string `json:"signIn,omitempty"`
	// Keys lists the keys the owner can type in Settings (ADR 0068): "xai" when the xAI key store is writable.
	Keys []string `json:"keys,omitempty"`
	// Settings lists the settings this host keeps and enforces (ADR 0068): "providers", the approved-model lists.
	Settings []string `json:"settings,omitempty"`
}

// Host is the optional `host` of /v1/status (contract/runtime-http/host.schema.json): the desktop host names itself,
// its version and platform; the web hosts send none, which is how the app tells it runs in a browser.
type Host struct {
	Kind    string `json:"kind"`
	Version string `json:"version"`
	OS      string `json:"os"`
	Arch    string `json:"arch"`
}

// Server is the runtime of one desktop app.
type Server struct {
	constants    Constants
	keys         KeySource
	base         string
	httpClient   *http.Client
	capabilities Capabilities
	models       modelCache
	mediaDir     string
	saver        Saver
	exportName   *regexp.Regexp
	host         Host
	orKeys       KeyStore
	orBase       string
	orModels     modelCache
	openURL      func(string) error
	auth         signInState
	limits       limits
	settingsFile string
	settingsMu   sync.Mutex
	orPictures   bool
}

// Options configure a Server. BaseURL defaults to the contract's xAI base URL; Client to a client without redirects.
type Options struct {
	Constants    Constants
	Keys         KeySource
	BaseURL      string
	Client       *http.Client
	Capabilities *Capabilities
	// MediaDir is the picture store (capabilities.files); default <UserConfigDir>/BuildY/media.
	MediaDir string
	// Saver keeps the ZIP export (capabilities.saveFile); nil: no native save, the browser downloads it.
	Saver Saver
	// Version is the stamped app version (appid.Version); one that does not match constants.host.version, or none,
	// is reported as constants.host.devVersion.
	Version string
	// OpenRouter keeps the key of the OpenRouter sign-in (ADR 0067); nil: no sign-in offered, xAI only.
	OpenRouter KeyStore
	// OpenRouterBaseURL defaults to the contract's OpenRouter base URL.
	OpenRouterBaseURL string
	// OpenURL opens the sign-in page in the system browser (Wails app.Browser.OpenURL); nil: the webview gets the URL only.
	OpenURL func(string) error
	// SettingsFile keeps the approved-model lists (ADR 0068); default <UserConfigDir>/BuildY/providers.json.
	SettingsFile string
	// OpenRouterMedia overrides constants.openrouter.mediaEnabled (the contract runner's env.openrouterMedia).
	OpenRouterMedia *bool
}

// DesktopCapabilities is what this build implements: the agent turn runs in the webview over the provider forward
// (forward.go); Media results are kept in a local picture store (media.go); the ZIP export is saved natively when a
// Saver is configured (export.go; SaveFile follows Options.Saver).
var DesktopCapabilities = Capabilities{Loop: "client", Files: true, SaveFile: true, Settings: []string{"providers"}}

// New builds the runtime.
func New(o Options) *Server {
	s := &Server{constants: o.Constants, keys: o.Keys, base: strings.TrimRight(strings.TrimSpace(o.BaseURL), "/"), httpClient: o.Client, capabilities: DesktopCapabilities, mediaDir: o.MediaDir, saver: o.Saver, exportName: compileExportName(o.Constants.Export.Name)}
	s.capabilities.SaveFile = o.Saver != nil
	if s.mediaDir == "" {
		s.mediaDir = media.DefaultDir()
	}
	if o.Capabilities != nil {
		s.capabilities = *o.Capabilities
	}
	s.host = hostOf(o.Constants, o.Version)
	s.orKeys, s.orBase, s.openURL = o.OpenRouter, strings.TrimRight(strings.TrimSpace(o.OpenRouterBaseURL), "/"), o.OpenURL
	if s.orKeys != nil && o.Capabilities == nil {
		s.capabilities.SignIn = []string{"openrouter"}
	}
	if _, writable := o.Keys.(KeyStore); writable && o.Capabilities == nil {
		s.capabilities.Keys = []string{"xai"}
	}
	s.settingsFile = o.SettingsFile
	if s.settingsFile == "" {
		s.settingsFile = filepath.Join(filepath.Dir(media.DefaultDir()), "providers.json")
	}
	s.orPictures = o.Constants.OpenRouter.MediaEnabled
	if o.OpenRouterMedia != nil {
		s.orPictures = *o.OpenRouterMedia
	}
	return s
}

// hostOf is the status host of this build: the contract's kind, the stamped version when it has the contract's shape
// (else the dev version), and the platform Go was built for.
func hostOf(c Constants, version string) Host {
	h := Host{Kind: c.Host.Kind, Version: c.Host.DevVersion, OS: goruntime.GOOS, Arch: goruntime.GOARCH}
	if re, err := regexp.Compile(c.Host.Version); err == nil && version != "" && re.MatchString(version) {
		h.Version = version
	}
	return h
}

func (s *Server) baseURL() string {
	if s.base != "" {
		return s.base
	}
	return s.constants.XAI.DefaultBaseURL
}

// client never follows a redirect, whichever client was configured: the key must not travel to another host.
func (s *Server) client() *http.Client {
	c := http.Client{}
	if s.httpClient != nil {
		c = *s.httpClient
	}
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &c
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
	case method == http.MethodPost && (path == mediaPath || strings.HasPrefix(path, mediaPath+"/")):
		s.media(w, r)
	case path == designPath:
		s.design(w, r, path, method)
	case path == exportPath:
		s.export(w, r, method)
	case strings.HasPrefix(path, filesPrefix):
		s.files(w, r, path, method)
	case strings.HasPrefix(path, forwardPrefix):
		s.forward(w, r, path, method)
	case strings.HasPrefix(path, orForwardPrefix) && s.orKeys != nil:
		s.orForward(w, r, path, method)
	case strings.HasPrefix(path, settingsPrefix):
		s.settingsRoute(w, r, path, method)
	case strings.HasPrefix(path, keysPrefix):
		s.keysRoute(w, r, path, method)
	case strings.HasPrefix(path, authPrefix):
		s.authRoute(w, r, path, method)
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
