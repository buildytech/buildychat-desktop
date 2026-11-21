package runtime

// The desktop runner of the runtime HTTP contract (buildytech/chat contract/runtime-http, ADR 0065): every case whose
// surfaces include "desktop", from the embedded pinned submodule, through the same middleware the app uses, with the
// provider stubbed from the case. Files of later phases are skipped by name until their handlers exist.

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"

	buildychat "github.com/buildytech/buildychat-desktop"
)

const (
	testKey  = "contract-test-key-not-real"
	testBase = "https://xai.test/v1"
)

// implemented lists the case files this phase serves; the rest are skipped with their phase.
var implemented = map[string]bool{"status.json": true, "router.json": true}

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
		Method string          `json:"method"`
		URL    string          `json:"url"`
		Auth   string          `json:"auth"`
		JSON   json.RawMessage `json:"json"`
	} `json:"match"`
	Reply reply `json:"reply"`
}

type contractCase struct {
	Name     string   `json:"name"`
	Surfaces []string `json:"surfaces"`
	Env      struct {
		Key bool `json:"key"`
	} `json:"env"`
	Request struct {
		Method  string            `json:"method"`
		Path    string            `json:"path"`
		Query   string            `json:"query"`
		Headers map[string]string `json:"headers"`
		JSON    json.RawMessage   `json:"json"`
		Text    json.RawMessage   `json:"text"`
		Bytes   *bytesSpec        `json:"bytes"`
	} `json:"request"`
	Upstream []upstream `json:"upstream"`
	Expect   struct {
		Status      int               `json:"status"`
		JSON        json.RawMessage   `json:"json"`
		Headers     map[string]string `json:"headers"`
		Bytes       *bytesSpec        `json:"bytes"`
		NotContains []string          `json:"notContains"`
	} `json:"expect"`
}

type caseFile struct {
	Cases []contractCase `json:"cases"`
}

func macros(c Constants) map[string]string {
	return map[string]string{"$KEY": testKey, "$CONTRACT": c.Contract, "$CANDIDATE": c.CandidateChatModel, "$IMAGE_MODEL": c.Images.Model}
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
// "$absent", "$boolean", "$prefix:<text>".
func matches(actual any, expected any, path string, problems *[]string) {
	if s, ok := expected.(string); ok {
		if s == "$boolean" {
			if _, ok := actual.(bool); !ok {
				*problems = append(*problems, fmt.Sprintf("%s: expected a boolean, got %v", path, actual))
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
		want := u.Match.URL
		if strings.HasPrefix(want, "/") {
			want = testBase + want
		}
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
			body, _ = json.Marshal(v)
			if h.Get("content-type") == "" {
				h.Set("content-type", "application/json")
			}
		} else if rep.Bytes != nil {
			body = bytesOf(s.contract, rep.Bytes)
		} else if v, ok := decode(rep.Text); ok {
			body = []byte(fmt.Sprint(v))
		}
		return &http.Response{StatusCode: status, Header: h, Body: io.NopCloser(bytes.NewReader(body)), ContentLength: int64(len(body)), Request: r}, nil
	}
	s.problems = append(s.problems, "unexpected upstream "+r.Method+" "+url)
	return nil, errors.New("unexpected upstream call")
}

type fixedKey string

func (k fixedKey) Key() (string, string) {
	if k == "" {
		return "", ""
	}
	return string(k), "keychain"
}

func runCase(t *testing.T, contract fs.FS, constants Constants, c contractCase) []string {
	m := macros(constants)
	st := &stub{contract: contract, m: m, entries: c.Upstream, used: make([]bool, len(c.Upstream))}
	key := fixedKey("")
	if c.Env.Key {
		key = testKey
	}
	srv := New(Options{Constants: constants, Keys: key, BaseURL: testBase, Client: &http.Client{Transport: st}})
	static := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "web app", http.StatusTeapot) })
	handler := srv.Middleware(static)

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
	req := httptest.NewRequest(c.Request.Method, "wails://localhost"+target, body)
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
	if c.Expect.Status == http.StatusNoContent && len(raw) > 0 {
		problems = append(problems, "204 with a body")
	}
	words := append([]string{testKey}, c.Expect.NotContains...)
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
					t.Skip("handler comes in a later phase (media: phase 3)")
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
	if ran < 10 {
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
