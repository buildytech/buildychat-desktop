package runtime

// The standalone preview window (POST /v1/preview-window). The web app opens the page preview with window.open, which
// a Wails webview cannot do: WebView2 makes a popup that has no asset handler for http://wails.localhost (so the
// address fails to load), and WebKit refuses a popup it was not given. The desktop host opens the window itself: a
// second app window on the same asset server and the same webview storage, so the preview reads the saved session.
// Only an in-app preview address is accepted; the host never opens anything else.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
)

const previewWindowPath = "/v1/preview-window"

// maxPreviewPath bounds the address the web app may ask for.
const maxPreviewPath = 400

var previewSession = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

// PreviewWindow opens (or raises) the preview window of one in-app address and answers nothing else.
type PreviewWindow func(name, path string) error

// previewTarget accepts `/preview?session=<id>` and `/<slug>?preview&session=<id>` (previewWindowUrl in the web app):
// a path on this app, never the runtime, with a plain session id. It answers the address to open and a stable window name.
func previewTarget(raw string) (path, name string, ok bool) {
	if raw == "" || len(raw) > maxPreviewPath || !strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, "//") || strings.ContainsAny(raw, "\\\r\n\t ") {
		return "", "", false
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "" || u.Host != "" || u.User != nil || u.Fragment != "" || IsRuntimePath(u.Path) {
		return "", "", false
	}
	q := u.Query()
	session := q.Get("session")
	if !previewSession.MatchString(session) {
		return "", "", false
	}
	_, hasPreview := q["preview"]
	if u.Path != "/preview" && !hasPreview {
		return "", "", false
	}
	if strings.Contains(u.Path, "..") {
		return "", "", false
	}
	sum := sha256.Sum256([]byte(session))
	return raw, "preview-" + hex.EncodeToString(sum[:6]), true
}

func (s *Server) previewWindow(w http.ResponseWriter, r *http.Request, method string) {
	if method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": map[string]string{"code": "method_not_allowed", "message": "POST the preview address."}})
		return
	}
	if s.openWindow == nil {
		// The headless build has no window to open: the browser opens the address itself.
		writeJSON(w, http.StatusNotImplemented, map[string]any{"error": map[string]string{"code": "unsupported", "message": "This host cannot open a window."}})
		return
	}
	var body struct {
		Path string `json:"path"`
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, 4096))
	if err != nil || json.Unmarshal(data, &body) != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": map[string]string{"code": "invalid_json", "message": "Expected {\"path\": \"/<slug>?preview&session=<id>\"}."}})
		return
	}
	path, name, ok := previewTarget(body.Path)
	if !ok {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"error": map[string]string{"code": "invalid_path", "message": "Only a preview address of this app can be opened."}})
		return
	}
	if err := s.openWindow(name, path); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"error": map[string]string{"code": "open_failed", "message": "The preview window could not be opened."}})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
