package runtime

// Sign in with OpenRouter (ADR 0067, contract cases/openrouter-auth.json; https://openrouter.ai/docs/guides/overview/auth/oauth):
// POST /v1/auth/openrouter/start makes a PKCE verifier, its S256 challenge and a state, opens a one-shot loopback
// listener on a random port and the system browser at the auth page with callback http://localhost:<port>/cb/<state>.
// The callback reaches only that listener (never the app's origin), at most once and within the contract's TTL; the code
// is exchanged with the verifier at POST <base>/auth/keys and the user's key goes to the keychain. The key, the verifier
// and the code are never answered, logged or put in a URL the webview sees. POST /v1/auth/openrouter/signout deletes it.

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"html"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	authStartPath   = "/v1/auth/openrouter/start"
	authSignOutPath = "/v1/auth/openrouter/signout"
	authPrefix      = "/v1/auth/"
	exchangeTimeout = 20 * time.Second
)

// signIn is one started sign-in: its secrets live here only.
type signIn struct {
	state    string
	verifier string
	expires  time.Time
	servers  []*http.Server
	timer    *time.Timer
}

type signInState struct {
	mu      sync.Mutex
	pending *signIn
}

// pkceSecret is 32 random bytes, base64url without padding (43 characters).
func pkceSecret() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// challengeOf is the S256 code challenge of a verifier.
func challengeOf(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func (s *Server) signInPending() bool {
	s.auth.mu.Lock()
	defer s.auth.mu.Unlock()
	return s.auth.pending != nil && time.Now().Before(s.auth.pending.expires)
}

// closeSignIn ends a sign-in: its listeners close (after the answer that is being written) and its secrets are dropped.
func closeSignIn(p *signIn) {
	if p == nil {
		return
	}
	if p.timer != nil {
		p.timer.Stop()
	}
	for _, srv := range p.servers {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_ = srv.Shutdown(ctx)
		}()
	}
}

// Close ends a pending sign-in (the app quits, a test ends).
func (s *Server) Close() {
	s.auth.mu.Lock()
	p := s.auth.pending
	s.auth.pending = nil
	s.auth.mu.Unlock()
	closeSignIn(p)
}

func (s *Server) authRoute(w http.ResponseWriter, r *http.Request, path, method string) {
	switch path {
	case authStartPath, authSignOutPath:
	default:
		forwardError(w, http.StatusNotFound, "not_found", "Unknown runtime path.")
		return
	}
	if s.orKeys == nil {
		forwardError(w, http.StatusNotFound, "not_found", "This host offers no sign-in.")
		return
	}
	if method != http.MethodPost {
		w.Header().Set("allow", "POST")
		forwardError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Sign-in answers POST only.")
		return
	}
	if path == authSignOutPath {
		s.signOut(w)
		return
	}
	s.startSignIn(w)
}

func (s *Server) signOut(w http.ResponseWriter) {
	s.Close()
	if err := s.orKeys.Forget(); err != nil {
		forwardError(w, http.StatusInternalServerError, "keychain_failed", "The OpenRouter key could not be removed from the keychain.")
		return
	}
	s.orModels.clear()
	log.Printf(`{"runtime":"xai-grok","event":"openrouter_signout"}`)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "signedIn": s.orKey() != ""})
}

// listen opens the loopback listeners of one sign-in: 127.0.0.1 on a random port, and ::1 on the same port when the
// system has it (a browser may resolve localhost to either).
func listen() ([]net.Listener, int, error) {
	v4, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, 0, err
	}
	port := v4.Addr().(*net.TCPAddr).Port
	out := []net.Listener{v4}
	if v6, err := net.Listen("tcp", "[::1]:"+strconv.Itoa(port)); err == nil {
		out = append(out, v6)
	}
	return out, port, nil
}

func (s *Server) startSignIn(w http.ResponseWriter) {
	c := s.constants.OpenRouter
	listeners, port, err := listen()
	if err != nil {
		forwardError(w, http.StatusInternalServerError, "listen_failed", "The sign-in could not open its local callback.")
		return
	}
	p := &signIn{state: pkceSecret(), verifier: pkceSecret(), expires: time.Now().Add(time.Duration(c.Callback.TTLSeconds) * time.Second)}
	handler := s.callbackHandler()
	for _, ln := range listeners {
		srv := &http.Server{Handler: handler, ReadHeaderTimeout: 10 * time.Second}
		p.servers = append(p.servers, srv)
		go func() { _ = srv.Serve(ln) }()
	}
	callback := "http://" + c.Callback.Host + ":" + strconv.Itoa(port) + c.Callback.PathPrefix + p.state
	authURL := c.AuthURL + "?callback_url=" + url.QueryEscape(callback) + "&code_challenge=" + challengeOf(p.verifier) + "&code_challenge_method=S256"
	s.auth.mu.Lock()
	previous := s.auth.pending
	s.auth.pending = p
	p.timer = time.AfterFunc(time.Until(p.expires), func() {
		s.auth.mu.Lock()
		if s.auth.pending == p {
			s.auth.pending = nil
		}
		s.auth.mu.Unlock()
		closeSignIn(p)
	})
	s.auth.mu.Unlock()
	closeSignIn(previous)
	if s.openURL != nil {
		if err := s.openURL(authURL); err != nil {
			log.Printf(`{"runtime":"xai-grok","event":"openrouter_signin","opened":false}`)
		}
	}
	log.Printf(`{"runtime":"xai-grok","event":"openrouter_signin","started":true}`)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "pending": true, "url": authURL})
}

func callbackPage(w http.ResponseWriter, status int, title, text string) {
	w.Header().Set("content-type", "text/html; charset=utf-8")
	w.Header().Set("cache-control", "no-store")
	w.Header().Set("x-content-type-options", "nosniff")
	w.Header().Set("referrer-policy", "no-referrer")
	w.Header().Set("content-security-policy", "default-src 'none'; style-src 'unsafe-inline'")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, `<!doctype html><html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>`+
		html.EscapeString(title)+`</title><style>body{font:16px/1.5 system-ui,sans-serif;margin:0;display:grid;place-items:center;min-height:100vh;background:#0b0b0c;color:#ededed}main{max-width:32rem;padding:24px}h1{font-size:20px;margin:0 0 8px}</style></head><body><main><h1>`+
		html.EscapeString(title)+`</h1><p>`+html.EscapeString(text)+`</p></main></body></html>`)
}

// callbackHandler serves the loopback listener: GET <pathPrefix><state>?code=… once.
func (s *Server) callbackHandler() http.Handler {
	prefix := s.constants.OpenRouter.Callback.PathPrefix
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		state, isCallback := strings.CutPrefix(r.URL.Path, prefix)
		if !isCallback || state == "" || strings.Contains(state, "/") {
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodGet {
			w.Header().Set("allow", "GET")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		s.auth.mu.Lock()
		p := s.auth.pending
		if p != nil && !time.Now().Before(p.expires) {
			s.auth.pending = nil
			s.auth.mu.Unlock()
			closeSignIn(p)
			callbackPage(w, http.StatusBadRequest, "Sign-in expired", "This sign-in took too long. Start it again from BuildY Chat.")
			return
		}
		if p == nil || subtle.ConstantTimeCompare([]byte(state), []byte(p.state)) != 1 {
			s.auth.mu.Unlock()
			callbackPage(w, http.StatusBadRequest, "No sign-in is waiting", "BuildY Chat did not start this sign-in, or it was already used. Start it again from the app.")
			return
		}
		s.auth.pending = nil
		s.auth.mu.Unlock()
		defer closeSignIn(p)
		q := r.URL.Query()
		code := q.Get("code")
		if q.Get("error") != "" || code == "" || len(code) > 2048 {
			callbackPage(w, http.StatusBadRequest, "Sign-in cancelled", "OpenRouter did not grant access. You can close this tab and try again from BuildY Chat.")
			return
		}
		status := s.exchange(r.Context(), code, p.verifier)
		switch status {
		case http.StatusOK:
			log.Printf(`{"runtime":"xai-grok","event":"openrouter_signin","ok":true}`)
			callbackPage(w, http.StatusOK, "Signed in with OpenRouter", "BuildY Chat can use your OpenRouter account now. You can close this tab.")
		case http.StatusBadRequest:
			log.Printf(`{"runtime":"xai-grok","event":"openrouter_signin","ok":false,"code":"code_refused"}`)
			callbackPage(w, http.StatusBadRequest, "Sign-in failed", "OpenRouter refused the sign-in code (expired or already used). Start the sign-in again from BuildY Chat.")
		default:
			log.Printf(`{"runtime":"xai-grok","event":"openrouter_signin","ok":false,"status":%d}`, status)
			callbackPage(w, status, "Sign-in failed", "The sign-in could not be completed. Start it again from BuildY Chat.")
		}
	})
}

// exchange trades the code for the user's key and keeps it: 200 kept, 400 code refused, 502 OpenRouter failed or
// answered no key, 500 the keychain failed.
func (s *Server) exchange(ctx context.Context, code, verifier string) int {
	ctx, cancel := context.WithTimeout(ctx, exchangeTimeout)
	defer cancel()
	payload, _ := json.Marshal(map[string]string{"code": code, "code_verifier": verifier, "code_challenge_method": "S256"})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.orBaseURL()+s.constants.OpenRouter.KeysPath, bytes.NewReader(payload))
	if err != nil {
		return http.StatusBadGateway
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	res, err := s.client().Do(req)
	if err != nil {
		return http.StatusBadGateway
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 64<<10))
	if res.StatusCode >= 400 && res.StatusCode < 500 {
		return http.StatusBadRequest
	}
	if res.StatusCode != http.StatusOK {
		return http.StatusBadGateway
	}
	var answer struct {
		Key string `json:"key"`
	}
	if json.Unmarshal(raw, &answer) != nil || strings.TrimSpace(answer.Key) == "" {
		return http.StatusBadGateway
	}
	if err := s.orKeys.Store(answer.Key); err != nil {
		return http.StatusInternalServerError
	}
	s.orModels.clear()
	return http.StatusOK
}
