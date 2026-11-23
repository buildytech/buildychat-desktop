package runtime

// A provider per role (ADR 0068, contract cases/media-provider.json, cases/provider-settings.json, cases/keys-xai.json):
// status `providers`, the approved-model lists the owner keeps in Settings (this host's config, enforced here), the
// limit state of each provider, and the xAI key typed in Settings (OS keychain, never answered).

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode"
)

const (
	settingsPrefix  = "/v1/settings/"
	settingsPath    = "/v1/settings/providers"
	keysPrefix      = "/v1/keys/"
	xaiKeyPath      = "/v1/keys/xai"
	maxSettingsBody = 64_000
)

// ProviderEntry is one entry of the status `providers` (contract/runtime-http/providers.schema.json).
type ProviderEntry struct {
	ID     string         `json:"id"`
	State  string         `json:"state"`
	Key    KeyState       `json:"key"`
	Roles  []string       `json:"roles"`
	Models ProviderModels `json:"models"`
	// Pending is OpenRouter's sign-in waiting for its callback.
	Pending *bool `json:"pending,omitempty"`
}

type ProviderModels struct {
	Chat  []string `json:"chat"`
	Image []string `json:"image"`
}

// limitRE is isProviderLimit of packages/agent-manifests/src/roles.ts (a 403 that names a key or credit limit).
var limitRE = regexp.MustCompile(`(?i)key limit|credit|insufficient|spend(?:ing)? limit|quota|billing`)

func isProviderLimit(status int, snippet string) bool {
	return status == http.StatusPaymentRequired || (status == http.StatusForbidden && limitRE.MatchString(snippet))
}

// limits remembers, per provider, when it last answered a limit.
type limits struct {
	mu sync.Mutex
	at map[string]time.Time
}

func (l *limits) mark(id string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.at == nil {
		l.at = map[string]time.Time{}
	}
	l.at[id] = time.Now()
}

func (l *limits) clear(id string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.at, id)
}

func (l *limits) active(id string, ttl time.Duration) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	at, ok := l.at[id]
	return ok && time.Since(at) < ttl
}

func (s *Server) markLimit(id string) { s.limits.mark(id) }
func (s *Server) limited(id string) bool {
	return s.limits.active(id, time.Duration(s.constants.Providers.LimitTTLSeconds)*time.Second)
}

// noteProvider records what a provider answer says about its limit: a limit answer marks it, a success clears it.
func (s *Server) noteProvider(id string, status int, body []byte) {
	switch {
	case isProviderLimit(status, string(body)):
		s.markLimit(id)
	case status >= 200 && status < 300:
		s.limits.clear(id)
	}
}

// roles answers the roles this host serves with a provider (constants.providers.roleSupport; OpenRouter pictures only
// while switched on).
func (s *Server) roles(id string) []string {
	out := []string{}
	for _, role := range s.constants.Providers.RoleSupport[id] {
		if role == "media" && id == "openrouter" && !s.orPictures {
			continue
		}
		out = append(out, role)
	}
	if id == "openrouter" && s.orPictures && !contains(out, "media") {
		out = append(out, "media")
	}
	return out
}

// imageModel is the image model a provider draws with.
func (s *Server) imageModel(id string) string {
	if id == "openrouter" {
		return s.constants.OpenRouter.ImageModel
	}
	return s.constants.Images.Model
}

func nonNil(ids []string) []string {
	if ids == nil {
		return []string{}
	}
	return ids
}

// providerEntry builds one status entry from the provider's key and its discovery (nil: no key, not asked).
func (s *Server) providerEntry(id, source string, d *discovered) ProviderEntry {
	e := ProviderEntry{ID: id, Roles: s.roles(id), Models: ProviderModels{Chat: []string{}, Image: []string{}}}
	if id == "openrouter" {
		pending := s.signInPending()
		e.Pending = &pending
	}
	switch {
	case d == nil:
		e.State = "not_configured"
		return e
	}
	src := source
	e.Key = KeyState{Present: true, Source: &src}
	e.Models.Chat = nonNil(d.chatIDs)
	if contains(e.Roles, "media") {
		e.Models.Image = nonNil(d.imageIDs)
	}
	switch {
	case s.limited(id):
		e.State = "limit"
	case len(e.Models.Chat) > 0:
		e.State = "ready"
	default:
		e.State = "unavailable"
	}
	return e
}

// ---- approved-model lists ----

// approvedDoc is {provider: {kind: nil | ids}}.
type approvedDoc map[string]map[string]*[]string

func (s *Server) emptyApproved() approvedDoc {
	doc := approvedDoc{}
	for _, id := range s.constants.Providers.IDs {
		doc[id] = map[string]*[]string{}
		for _, kind := range s.constants.Providers.Kinds {
			doc[id][kind] = nil
		}
	}
	return doc
}

// approved reads the saved lists (a missing or unreadable file: nothing narrowed).
func (s *Server) approved() approvedDoc {
	s.settingsMu.Lock()
	defer s.settingsMu.Unlock()
	return s.readApproved()
}

func (s *Server) readApproved() approvedDoc {
	doc := s.emptyApproved()
	raw, err := os.ReadFile(s.settingsFile)
	if err != nil {
		return doc
	}
	var saved struct {
		Approved approvedDoc `json:"approved"`
	}
	if json.Unmarshal(raw, &saved) != nil {
		return doc
	}
	for id, kinds := range saved.Approved {
		if _, known := doc[id]; !known {
			continue
		}
		for kind, list := range kinds {
			if _, known := doc[id][kind]; known {
				doc[id][kind] = list
			}
		}
	}
	return doc
}

// approvedList answers the approved ids of a provider and kind, and whether the list narrows at all.
func (s *Server) approvedList(id, kind string) ([]string, bool) {
	list := s.approved()[id][kind]
	if list == nil {
		return nil, false
	}
	return *list, true
}

var errInvalidSettings = errors.New("invalid settings")

// mergeApproved checks a PUT body's `approved` and merges it into the saved lists (absent providers and kinds stay).
func (s *Server) mergeApproved(raw json.RawMessage) (approvedDoc, error) {
	var put map[string]json.RawMessage
	if json.Unmarshal(raw, &put) != nil || put == nil {
		return nil, errInvalidSettings
	}
	pattern, err := regexp.Compile(s.constants.Providers.Approved.IDPattern)
	if err != nil {
		return nil, err
	}
	s.settingsMu.Lock()
	defer s.settingsMu.Unlock()
	doc := s.readApproved()
	for id, rawKinds := range put {
		if _, known := doc[id]; !known {
			return nil, errInvalidSettings
		}
		var kinds map[string]json.RawMessage
		if json.Unmarshal(rawKinds, &kinds) != nil || kinds == nil {
			return nil, errInvalidSettings
		}
		for kind, rawList := range kinds {
			if _, known := doc[id][kind]; !known {
				return nil, errInvalidSettings
			}
			if strings.TrimSpace(string(rawList)) == "null" {
				doc[id][kind] = nil
				continue
			}
			var items []string
			if json.Unmarshal(rawList, &items) != nil || items == nil {
				return nil, errInvalidSettings
			}
			ids := []string{}
			for _, item := range items {
				item = strings.TrimSpace(item)
				if item == "" || contains(ids, item) {
					continue
				}
				if !pattern.MatchString(item) {
					return nil, errInvalidSettings
				}
				ids = append(ids, item)
			}
			if len(ids) > s.constants.Providers.Approved.MaxIDs {
				return nil, errInvalidSettings
			}
			doc[id][kind] = &ids
		}
	}
	if err := s.writeApproved(doc); err != nil {
		return nil, err
	}
	return doc, nil
}

func (s *Server) writeApproved(doc approvedDoc) error {
	raw, err := json.MarshalIndent(map[string]any{"approved": doc}, "", "\t")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.settingsFile), 0o700); err != nil {
		return err
	}
	tmp := s.settingsFile + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.settingsFile)
}

func settingsError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{"ok": false, "code": code, "message": message})
}

// settingsRoute is GET/PUT /v1/settings/providers.
func (s *Server) settingsRoute(w http.ResponseWriter, r *http.Request, path, method string) {
	if path != settingsPath {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": map[string]string{"code": "not_found", "message": "Unknown runtime path."}})
		return
	}
	switch method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "approved": s.approved()})
	case http.MethodPut:
		raw, err := io.ReadAll(io.LimitReader(r.Body, maxSettingsBody+1))
		if err != nil || len(raw) > maxSettingsBody {
			settingsError(w, http.StatusRequestEntityTooLarge, "too_large", "The settings are too large.")
			return
		}
		var body map[string]json.RawMessage
		if json.Unmarshal(raw, &body) != nil || body == nil {
			settingsError(w, http.StatusBadRequest, "invalid_json", "Send one JSON object.")
			return
		}
		doc, err := s.mergeApproved(body["approved"])
		if errors.Is(err, errInvalidSettings) {
			settingsError(w, http.StatusUnprocessableEntity, "invalid_request", "approved holds xai and openrouter, each with chat and image lists (null or model ids).")
			return
		}
		if err != nil {
			settingsError(w, http.StatusInternalServerError, "settings_unwritable", "The settings could not be saved on this computer.")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "approved": doc})
	default:
		w.Header().Set("allow", "GET, PUT")
		settingsError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Settings answer GET and PUT.")
	}
}

// ---- the xAI key typed in Settings ----

func (s *Server) xaiStore() KeyStore {
	store, _ := s.keys.(KeyStore)
	return store
}

func validKey(key string, max int) bool {
	if key == "" || len(key) > max {
		return false
	}
	for _, r := range key {
		if unicode.IsSpace(r) || !unicode.IsPrint(r) {
			return false
		}
	}
	return true
}

// keysRoute is PUT/DELETE /v1/keys/xai: the key goes to the keychain and is never answered.
func (s *Server) keysRoute(w http.ResponseWriter, r *http.Request, path, method string) {
	store := s.xaiStore()
	if path != xaiKeyPath || store == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"error": map[string]string{"code": "not_found", "message": "Unknown runtime path."}})
		return
	}
	switch method {
	case http.MethodPut:
		raw, err := io.ReadAll(io.LimitReader(r.Body, 8_000))
		var body struct {
			Key *string `json:"key"`
		}
		if err != nil || json.Unmarshal(raw, &body) != nil {
			settingsError(w, http.StatusBadRequest, "invalid_json", "Send one JSON object.")
			return
		}
		key := ""
		if body.Key != nil {
			key = strings.TrimSpace(*body.Key)
		}
		if !validKey(key, s.constants.Providers.XAIKeyMax) {
			settingsError(w, http.StatusUnprocessableEntity, "invalid_key", "Paste the xAI API key: one word, no spaces.")
			return
		}
		if err := store.Store(key); err != nil {
			settingsError(w, http.StatusInternalServerError, "keychain_unavailable", "The key could not be kept in this computer's keychain.")
			return
		}
	case http.MethodDelete:
		if err := store.Forget(); err != nil {
			settingsError(w, http.StatusInternalServerError, "keychain_unavailable", "The key could not be removed from this computer's keychain.")
			return
		}
	default:
		w.Header().Set("allow", "PUT, DELETE")
		settingsError(w, http.StatusMethodNotAllowed, "method_not_allowed", "The key is never read back: PUT stores it, DELETE removes it.")
		return
	}
	s.models.clear()
	s.limits.clear("xai")
	key, source := store.Key()
	answer := map[string]any{"ok": true, "present": key != "", "source": nil}
	if key != "" {
		answer["source"] = source
	}
	writeJSON(w, http.StatusOK, answer)
}
