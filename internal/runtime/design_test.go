package runtime

// Design mode (ADR 0070): the chat contract binds cases/design.json to the web hosts only, so the desktop runs the
// same cases here (the shared runner, whatever the case's surfaces) plus what only the desktop has: the OpenRouter
// provider, the approved list, the method and a key that must not come back.

import (
	"encoding/json"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	buildychat "github.com/buildytech/buildychat-desktop"
)

func TestDesignContractCases(t *testing.T) {
	contract := buildychat.Contract()
	constants, err := LoadConstants(contract)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := fs.ReadFile(contract, "cases/design.json")
	if err != nil {
		t.Skipf("no design cases in the pinned contract: %v", err)
	}
	var file caseFile
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatal(err)
	}
	if len(file.Cases) < 4 {
		t.Fatalf("only %d design cases", len(file.Cases))
	}
	for _, c := range file.Cases {
		t.Run(c.Name, func(t *testing.T) {
			for _, p := range runCase(t, contract, constants, c) {
				t.Error(p)
			}
		})
	}
}

func designRequest(t *testing.T, srv *Server, method, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(method, "wails://localhost/runtime/v1/design", strings.NewReader(body))
	srv.Middleware(http.NotFoundHandler()).ServeHTTP(rec, req)
	return rec
}

func TestDesignIsNotA404(t *testing.T) {
	constants, err := LoadConstants(buildychat.Contract())
	if err != nil {
		t.Fatal(err)
	}
	srv := New(Options{Constants: constants, Keys: fixedKey("")})
	body := `{"modelId":"` + constants.CandidateChatModel + `","messages":[{"role":"user","content":"A bakery brand"}]}`
	if rec := designRequest(t, srv, http.MethodPost, body); rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "xai_api_key_missing") {
		t.Fatalf("no key: %d %s", rec.Code, rec.Body.String())
	}
	if rec := designRequest(t, srv, http.MethodGet, ""); rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET: %d", rec.Code)
	}
	if rec := designRequest(t, srv, http.MethodPost, "{"); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad json: %d", rec.Code)
	}
	if rec := designRequest(t, srv, http.MethodPost, `{"modelId":"m","messages":[{"role":"tool","content":"x"}]}`); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("bad role: %d", rec.Code)
	}
}
