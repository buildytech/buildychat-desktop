package runtime

// The desktop runner of the runtime HTTP contract (buildytech/chat contract/runtime-http, ADR 0065): every case whose
// surfaces include "desktop", from the embedded pinned submodule, through the same middleware the app uses, with the
// provider stubbed from the case (upstream replies expand the macros too), each case with an empty picture store
// (or the case's `store`). Files of later phases are skipped by name until their handlers exist.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	buildychat "github.com/buildytech/buildychat-desktop"
	"github.com/buildytech/buildychat-desktop/internal/media"
)

const (
	testKey  = "contract-test-key-not-real"
	testBase = "https://xai.test/v1"
	// The OpenRouter side of the cases (ADR 0067): the user's key, the OpenRouter base, a seeded sign-in's verifier.
	testORKey    = "contract-openrouter-key-not-real"
	testORBase   = "https://openrouter.test/api/v1"
	testVerifier = "contract-verifier-0000000000000000000000000000"
)

// implemented lists the case files this phase serves; the rest are skipped with their phase.
var implemented = map[string]bool{"status.json": true, "router.json": true, "xai-forward.json": true,
	"media-generate.json": true, "media-asset.json": true, "media-encode.json": true, "media-files.json": true, "export.json": true,
	"openrouter-auth.json": true, "openrouter-forward.json": true, "media-openrouter.json": true}

type bytesSpec struct {
	Hex     *string `json:"hex"`
	Size    *int    `json:"size"`
	Fill    *int    `json:"fill"`
	Picture *string `json:"picture"`
}

type reply struct {
	Status  int               `json:"status"`
	Headers map[string]string `json:"headers"`
	JSON    json.RawMessage   `json:"json"`
	Text    json.RawMessage   `json:"text"`
	Bytes   *bytesSpec        `json:"bytes"`
	Throw   string            `json:"throw"`
}

type upstream struct {
	Match struct {
		Method  string            `json:"method"`
		URL     string            `json:"url"`
		Auth    string            `json:"auth"`
		Headers map[string]string `json:"headers"`
		JSON    json.RawMessage   `json:"json"`
	} `json:"match"`
	Reply reply `json:"reply"`
}

type contractCase struct {
	Name     string   `json:"name"`
	Surfaces []string `json:"surfaces"`
	Env      struct {
		Key           bool   `json:"key"`
		Save          string `json:"save"`
		OpenRouter    bool   `json:"openrouter"`
		PendingSignIn *struct {
			State    string `json:"state"`
			Verifier string `json:"verifier"`
			Expired  bool   `json:"expired"`
		} `json:"pendingSignIn"`
	} `json:"env"`
	Exports []string `json:"exports"`
	Request struct {
		Method   string            `json:"method"`
		Path     string            `json:"path"`
		Query    string            `json:"query"`
		Headers  map[string]string `json:"headers"`
		JSON     json.RawMessage   `json:"json"`
		Text     json.RawMessage   `json:"text"`
		Bytes    *bytesSpec        `json:"bytes"`
		Listener string            `json:"listener"`
	} `json:"request"`
	Store    []string   `json:"store"`
	Upstream []upstream `json:"upstream"`
	Expect   struct {
		Status       int               `json:"status"`
		JSON         json.RawMessage   `json:"json"`
		Headers      map[string]string `json:"headers"`
		Bytes        *bytesSpec        `json:"bytes"`
		NotContains  []string          `json:"notContains"`
		Saved        json.RawMessage   `json:"saved"`
		Opened       string            `json:"opened"`
		SignedIn     *bool             `json:"signedIn"`
		PendingAfter *bool             `json:"pendingAfter"`
	} `json:"expect"`
}

type caseFile struct {
	Cases []contractCase `json:"cases"`
}

func macros(contract fs.FS, c Constants) map[string]string {
	m := map[string]string{"$KEY": testKey, "$CONTRACT": c.Contract, "$CANDIDATE": c.CandidateChatModel, "$IMAGE_MODEL": c.Images.Model,
		"$ORKEY": testORKey, "$VERIFIER": testVerifier}
	// $B64:<picture>: the base64 of a file of pictures/ (OpenRouter's draws answer pictures inline).
	pictures, _ := fs.Glob(contract, "pictures/*")
	for _, name := range pictures {
		raw, err := fs.ReadFile(contract, name)
		if err != nil {
			panic(err)
		}
		m["$B64:"+strings.TrimPrefix(name, "pictures/")] = base64.StdEncoding.EncodeToString(raw)
	}
	return m
}

// upstreamURL resolves a case's upstream URL: "/…" on the xAI base, "$OPENROUTER/…" on the OpenRouter base.
func upstreamURL(want string) string {
	if rest, ok := strings.CutPrefix(want, "$OPENROUTER/"); ok {
		return testORBase + "/" + rest
	}
	if strings.HasPrefix(want, "/") {
		return testBase + want
	}
	return want
}

var fileMacro = regexp.MustCompile(`\$FILE:([A-Za-z0-9._-]+)`)

// expandFiles replaces $FILE:<picture> with the store address of that file of pictures/ (sha256 of its bytes).
func expandFiles(contract fs.FS, text string) string {
	return fileMacro.ReplaceAllStringFunc(text, func(m string) string {
		name := m[len("$FILE:"):]
		raw, err := fs.ReadFile(contract, "pictures/"+name)
		if err != nil {
			panic(err)
		}
		sum := sha256.Sum256(raw)
		ext := name[strings.LastIndexByte(name, '.')+1:]
		if ext == "jpeg" {
			ext = "jpg"
		}
		return media.FilesPrefix + hex.EncodeToString(sum[:]) + "." + ext
	})
}

// expandFilesRaw applies expandFiles to a JSON document (addresses need no escaping).
func expandFilesRaw(contract fs.FS, raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return raw
	}
	return json.RawMessage(expandFiles(contract, string(raw)))
}

// expand resolves the value macros and {"$repeat": [text, count]}.
func expand(v any, m map[string]string) any {
	switch x := v.(type) {
	case string:
		if r, ok := m[x]; ok {
			return r
		}
		return x
	case []any:
		out := make([]any, len(x))
		for i, item := range x {
			out[i] = expand(item, m)
		}
		return out
	case map[string]any:
		if rep, ok := x["$repeat"].([]any); ok && len(rep) == 2 {
			if s, ok := rep[0].(string); ok {
				if n, ok := rep[1].(float64); ok {
					return strings.Repeat(s, int(n))
				}
			}
		}
		out := map[string]any{}
		for k, item := range x {
			out[k] = expand(item, m)
		}
		return out
	}
	return v
}

func decode(raw json.RawMessage) (any, bool) {
	if len(raw) == 0 {
		return nil, false
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		panic(err)
	}
	return v, true
}

func bytesOf(contract fs.FS, s *bytesSpec) []byte {
	switch {
	case s.Picture != nil:
		b, err := fs.ReadFile(contract, "pictures/"+*s.Picture)
		if err != nil {
			panic(err)
		}
		return b
	case s.Hex != nil:
		b, err := hex.DecodeString(*s.Hex)
		if err != nil {
			panic(err)
		}
		return b
	}
	n, fill := 0, 0
	if s.Size != nil {
		n = *s.Size
	}
	if s.Fill != nil {
		fill = *s.Fill
	}
	return bytes.Repeat([]byte{byte(fill)}, n)
}

// matches is the subset matcher of the TS runner: objects by expected keys, arrays by length and element,
// "$absent", "$boolean", "$prefix:<text>", "$match:<regex>".
func matches(actual any, expected any, path string, problems *[]string) {
	if s, ok := expected.(string); ok {
		if s == "$boolean" {
			if _, ok := actual.(bool); !ok {
				*problems = append(*problems, fmt.Sprintf("%s: expected a boolean, got %v", path, actual))
			}
			return
		}
		if strings.HasPrefix(s, "$match:") {
			a, ok := actual.(string)
			re, err := regexp.Compile(s[7:])
			if err != nil || !ok || !re.MatchString(a) {
				*problems = append(*problems, fmt.Sprintf("%s: expected %s, got %v", path, s, actual))
			}
			return
		}
		if strings.HasPrefix(s, "$prefix:") {
			if a, ok := actual.(string); !ok || !strings.HasPrefix(a, s[8:]) {
				*problems = append(*problems, fmt.Sprintf("%s: expected %s, got %v", path, s, actual))
			}
			return
		}
	}
	switch e := expected.(type) {
	case []any:
		a, ok := actual.([]any)
		if !ok || len(a) != len(e) {
			*problems = append(*problems, fmt.Sprintf("%s: expected %d items, got %v", path, len(e), actual))
			return
		}
		for i := range e {
			matches(a[i], e[i], fmt.Sprintf("%s/%d", path, i), problems)
		}
	case map[string]any:
		a, ok := actual.(map[string]any)
		if !ok {
			*problems = append(*problems, fmt.Sprintf("%s: expected an object, got %v", path, actual))
			return
		}
		keys := make([]string, 0, len(e))
		for k := range e {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			if e[k] == "$absent" {
				if _, has := a[k]; has {
					*problems = append(*problems, fmt.Sprintf("%s/%s: expected absent", path, k))
				}
				continue
			}
			matches(a[k], e[k], path+"/"+k, problems)
		}
	default:
		if fmt.Sprint(actual) != fmt.Sprint(expected) || (actual == nil) != (expected == nil) {
			*problems = append(*problems, fmt.Sprintf("%s: expected %v, got %v", path, expected, actual))
		}
	}
}

// stub answers the provider and image-host calls of one case (any order, each entry once).
type stub struct {
	mu       sync.Mutex
	contract fs.FS
	m        map[string]string
	entries  []upstream
	used     []bool
	problems []string
}

func (s *stub) RoundTrip(r *http.Request) (*http.Response, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	url := r.URL.String()
	for i, u := range s.entries {
		want := upstreamURL(u.Match.URL)
		if s.used[i] || u.Match.Method != r.Method || want != url {
			continue
		}
		s.used[i] = true
		auth := r.Header.Get("Authorization")
		if u.Match.Auth == "bearer" && auth != "Bearer "+testKey {
			s.problems = append(s.problems, url+": expected the bearer key")
		}
		if u.Match.Auth == "none" && auth != "" {
			s.problems = append(s.problems, url+": expected no authorization")
		}
		if u.Match.Auth == "openrouter" && auth != "Bearer "+testORKey {
			s.problems = append(s.problems, url+": expected the OpenRouter key")
		}
		for name, v := range u.Match.Headers {
			got, has := r.Header.Get(name), len(r.Header.Values(name)) > 0
			if (v == "$absent" && has) || (v != "$absent" && got != v) {
				s.problems = append(s.problems, fmt.Sprintf("%s: header %s expected %s, got %q", url, name, v, got))
			}
		}
		if want, ok := decode(u.Match.JSON); ok {
			var got any
			if r.Body != nil {
				raw, _ := io.ReadAll(r.Body)
				_ = json.Unmarshal(raw, &got)
			}
			matches(got, expand(want, s.m), "upstream "+u.Match.URL, &s.problems)
		}
		rep := u.Reply
		if rep.Throw != "" {
			return nil, errors.New("stubbed " + rep.Throw + " failure")
		}
		status := rep.Status
		if status == 0 {
			status = 200
		}
		h := http.Header{}
		for k, v := range rep.Headers {
			h.Set(k, v)
		}
		var body []byte
		if v, ok := decode(rep.JSON); ok {
			body, _ = json.Marshal(expand(v, s.m))
			if h.Get("content-type") == "" {
				h.Set("content-type", "application/json")
			}
		} else if rep.Bytes != nil {
			body = bytesOf(s.contract, rep.Bytes)
		} else if v, ok := decode(rep.Text); ok {
			body = []byte(fmt.Sprint(expand(v, s.m)))
		}
		return &http.Response{StatusCode: status, Header: h, Body: io.NopCloser(bytes.NewReader(body)), ContentLength: int64(len(body)), Request: r}, nil
	}
	s.problems = append(s.problems, "unexpected upstream "+r.Method+" "+url)
	return nil, errors.New("unexpected upstream call")
}

// memoryKeys is the OpenRouter keychain item of a case.
type memoryKeys struct {
	mu  sync.Mutex
	key string
}

func (k *memoryKeys) Key() (string, string) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.key == "" {
		return "", ""
	}
	return k.key, "keychain"
}

func (k *memoryKeys) Store(key string) error {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.key = strings.TrimSpace(key)
	return nil
}

func (k *memoryKeys) Forget() error {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.key = ""
	return nil
}

type fixedKey string

func (k fixedKey) Key() (string, string) {
	if k == "" {
		return "", ""
	}
	return string(k), "keychain"
}

func runCase(t *testing.T, contract fs.FS, constants Constants, c contractCase) []string {
	m := macros(contract, constants)
	c.Request.Path = expandFiles(contract, c.Request.Path)
	c.Request.Query = expandFiles(contract, c.Request.Query)
	c.Request.JSON = expandFilesRaw(contract, c.Request.JSON)
	c.Expect.JSON = expandFilesRaw(contract, c.Expect.JSON)
	st := &stub{contract: contract, m: m, entries: c.Upstream, used: make([]bool, len(c.Upstream))}
	key := fixedKey("")
	if c.Env.Key {
		key = testKey
	}
	dir := t.TempDir()
	for _, name := range c.Store {
		raw, err := fs.ReadFile(contract, "pictures/"+name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := (media.Store{Dir: dir}).Put(raw, media.TypeOf(expandFiles(contract, "$FILE:"+name))); err != nil {
			t.Fatal(err)
		}
	}
	exports := t.TempDir()
	for _, name := range c.Exports {
		if err := os.WriteFile(filepath.Join(exports, name), []byte("an earlier export"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	var saver Saver = DirSaver{Dir: exports}
	switch c.Env.Save {
	case "cancel":
		saver = SaverFunc(func(context.Context, string, []byte) (string, error) { return "", ErrCancelled })
	case "none":
		saver = nil
	}
	orKeys := &memoryKeys{}
	if c.Env.OpenRouter {
		orKeys.key = testORKey
	}
	var opened []string
	srv := New(Options{Constants: constants, Keys: key, BaseURL: testBase, Client: &http.Client{Transport: st}, MediaDir: dir, Saver: saver,
		OpenRouter: orKeys, OpenRouterBaseURL: testORBase, OpenURL: func(u string) error { opened = append(opened, u); return nil }})
	defer srv.Close()
	if p := c.Env.PendingSignIn; p != nil {
		expires := time.Now().Add(time.Minute)
		if p.Expired {
			expires = time.Now().Add(-time.Second)
		}
		verifier := p.Verifier
		if v, ok := m[verifier]; ok {
			verifier = v
		}
		srv.auth.pending = &signIn{state: p.State, verifier: verifier, expires: expires}
	}
	static := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "web app", http.StatusTeapot) })
	handler := srv.Middleware(static)
	origin := "wails://localhost"
	if c.Request.Listener == "callback" {
		// The loopback listener of the sign-in, not the app's origin.
		handler, origin = srv.callbackHandler(), "http://localhost:51423"
	}

	var body io.Reader
	headers := http.Header{}
	for k, v := range c.Request.Headers {
		headers.Set(k, v)
	}
	if v, ok := decode(c.Request.JSON); ok {
		raw, _ := json.Marshal(expand(v, m))
		body = bytes.NewReader(raw)
		if headers.Get("content-type") == "" {
			headers.Set("content-type", "application/json")
		}
	} else if v, ok := decode(c.Request.Text); ok {
		body = strings.NewReader(fmt.Sprint(expand(v, m)))
	} else if c.Request.Bytes != nil {
		body = bytes.NewReader(bytesOf(contract, c.Request.Bytes))
	}
	target := c.Request.Path
	if c.Request.Query != "" {
		target += "?" + c.Request.Query
	}
	req := httptest.NewRequest(c.Request.Method, origin+target, body)
	req.Header = headers
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	problems := append([]string{}, st.problems...)
	raw := rec.Body.Bytes()
	text := string(raw)
	if rec.Code != c.Expect.Status {
		problems = append(problems, fmt.Sprintf("status: expected %d, got %d %.200s", c.Expect.Status, rec.Code, text))
	}
	for k, v := range c.Expect.Headers {
		if got := rec.Header().Get(k); got != v {
			problems = append(problems, fmt.Sprintf("header %s: expected %s, got %s", k, v, got))
		}
	}
	if want, ok := decode(c.Expect.JSON); ok {
		var got any
		if err := json.Unmarshal(raw, &got); err != nil {
			problems = append(problems, "body is not JSON: "+text)
		} else {
			matches(got, expand(want, m), "", &problems)
		}
	}
	if c.Expect.Bytes != nil && !bytes.Equal(raw, bytesOf(contract, c.Expect.Bytes)) {
		problems = append(problems, "body bytes differ")
	}
	if len(c.Expect.Saved) > 0 {
		problems = append(problems, checkSaved(contract, exports, c.Exports, c.Expect.Saved)...)
	}
	if c.Expect.Status == http.StatusNoContent && len(raw) > 0 {
		problems = append(problems, "204 with a body")
	}
	if c.Expect.Opened != "" {
		if len(opened) != 1 {
			problems = append(problems, fmt.Sprintf("opened: expected one URL handed to the browser, got %d", len(opened)))
		} else {
			matches(opened[0], c.Expect.Opened, "opened", &problems)
		}
	}
	if c.Expect.SignedIn != nil {
		if got, _ := orKeys.Key(); (got == testORKey) != *c.Expect.SignedIn {
			problems = append(problems, fmt.Sprintf("signedIn: expected %v", *c.Expect.SignedIn))
		}
	}
	if c.Expect.PendingAfter != nil && srv.signInPending() != *c.Expect.PendingAfter {
		problems = append(problems, fmt.Sprintf("pendingAfter: expected %v", *c.Expect.PendingAfter))
	}
	words := append([]string{testKey, testORKey}, c.Expect.NotContains...)
	for _, w := range words {
		if r, ok := m[w]; ok {
			w = r
		}
		if strings.Contains(text, w) {
			problems = append(problems, "the answer contains a forbidden word")
		}
	}
	for i, u := range c.Upstream {
		if !st.used[i] {
			problems = append(problems, fmt.Sprintf("upstream %s %s was not called", u.Match.Method, u.Match.URL))
		}
	}
	return problems
}

// checkSaved compares the export folder with expect.saved: null (nothing new written) or {name, bytes}.
func checkSaved(contract fs.FS, dir string, before []string, want json.RawMessage) []string {
	entries, _ := os.ReadDir(dir)
	var written []string
	for _, e := range entries {
		if !slices.Contains(before, e.Name()) {
			written = append(written, e.Name())
		}
	}
	if string(want) == "null" {
		if len(written) > 0 {
			return []string{fmt.Sprintf("saved: expected nothing written, got %v", written)}
		}
		return nil
	}
	var spec struct {
		Name  string     `json:"name"`
		Bytes *bytesSpec `json:"bytes"`
	}
	if err := json.Unmarshal(want, &spec); err != nil {
		return []string{"saved: " + err.Error()}
	}
	if len(written) != 1 || written[0] != spec.Name {
		return []string{fmt.Sprintf("saved: expected %s written, got %v", spec.Name, written)}
	}
	if spec.Bytes != nil {
		got, _ := os.ReadFile(filepath.Join(dir, spec.Name))
		if !bytes.Equal(got, bytesOf(contract, spec.Bytes)) {
			return []string{"saved: the file's bytes differ"}
		}
	}
	return nil
}

func desktop(c contractCase) bool {
	if len(c.Surfaces) == 0 {
		return true
	}
	for _, s := range c.Surfaces {
		if s == "desktop" {
			return true
		}
	}
	return false
}

func TestRuntimeHTTPContract(t *testing.T) {
	contract := buildychat.Contract()
	constants, err := LoadConstants(contract)
	if err != nil {
		t.Fatal(err)
	}
	names, err := fs.Glob(contract, "cases/*.json")
	if err != nil || len(names) == 0 {
		t.Fatalf("no contract cases embedded (git submodule update --init): %v", err)
	}
	ran := 0
	for _, name := range names {
		raw, err := fs.ReadFile(contract, name)
		if err != nil {
			t.Fatal(err)
		}
		var file caseFile
		if err := json.Unmarshal(raw, &file); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		base := strings.TrimPrefix(name, "cases/")
		for _, c := range file.Cases {
			if !desktop(c) {
				continue
			}
			c := c
			t.Run(base+": "+c.Name, func(t *testing.T) {
				if !implemented[base] {
					t.Skip("handler comes in a later phase")
				}
				for _, p := range runCase(t, contract, constants, c) {
					t.Error(p)
				}
			})
			if implemented[base] {
				ran++
			}
		}
	}
	t.Logf("desktop contract cases run: %d", ran)
	if ran < 90 {
		t.Fatalf("only %d desktop cases ran", ran)
	}
}

func TestStaticRequestsReachTheWebApp(t *testing.T) {
	constants, err := LoadConstants(buildychat.Contract())
	if err != nil {
		t.Fatal(err)
	}
	srv := New(Options{Constants: constants, Keys: fixedKey("")})
	handler := srv.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusTeapot) }))
	for _, path := range []string{"/", "/index.html", "/_app/x.js", "/runtimes", "/apiary"} {
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "wails://localhost"+path, nil))
		if rec.Code != http.StatusTeapot {
			t.Errorf("%s: went to the runtime (%d)", path, rec.Code)
		}
	}
}
