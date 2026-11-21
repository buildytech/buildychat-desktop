package runtime

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"sync"
	"time"
)

// discovered mirrors apps/xai-runtime/src/discover.ts (DiscoveredModels).
type discovered struct {
	ok        bool
	code      string
	ids       []string
	chatIDs   []string
	imageIDs  []string
	candidate *string
}

var imageModelName = regexp.MustCompile(`(?i)imagine|image`)

// idsFromPayload reads data[] or models[] rows: a string, or an object's id (else name).
func idsFromPayload(payload any) []string {
	rec, ok := payload.(map[string]any)
	if !ok {
		return nil
	}
	rows, ok := rec["data"].([]any)
	if !ok {
		rows, _ = rec["models"].([]any)
	}
	out := []string{}
	for _, row := range rows {
		switch v := row.(type) {
		case string:
			if v != "" {
				out = append(out, v)
			}
		case map[string]any:
			id := ""
			if s, ok := v["id"].(string); ok && s != "" {
				id = s
			} else if s, ok := v["name"].(string); ok {
				id = s
			}
			if id != "" {
				out = append(out, id)
			}
		}
	}
	return out
}

func unique(lists ...[]string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, list := range lists {
		for _, id := range list {
			if id != "" && !seen[id] {
				seen[id] = true
				out = append(out, id)
			}
		}
	}
	return out
}

func contains(list []string, id string) bool {
	for _, v := range list {
		if v == id {
			return true
		}
	}
	return false
}

type listed struct {
	status  int
	payload any
	err     error
}

func (s *Server) getJSON(ctx context.Context, path, key string) listed {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.baseURL()+path, nil)
	if err != nil {
		return listed{err: err}
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Accept", "application/json")
	res, err := s.client().Do(req)
	if err != nil {
		return listed{err: err}
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	var payload any
	if json.Unmarshal(raw, &payload) != nil {
		payload = nil
	}
	return listed{status: res.StatusCode, payload: payload}
}

// discover lists the key's models (GET /models, /language-models, /image-generation-models), classified as in TS.
func (s *Server) discover(ctx context.Context, key string) discovered {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	paths := s.constants.XAI.Discovery
	results := make([]listed, len(paths))
	var wg sync.WaitGroup
	for i, path := range paths {
		wg.Add(1)
		go func(i int, path string) {
			defer wg.Done()
			results[i] = s.getJSON(ctx, path, key)
		}(i, path)
	}
	wg.Wait()
	for _, r := range results {
		if r.err != nil {
			return discovered{code: "xai_models_list_unavailable", ids: []string{}, chatIDs: []string{}, imageIDs: []string{}}
		}
	}
	models, language, images := results[0], results[1], results[2]
	if models.status != 200 && language.status != 200 {
		return discovered{code: "xai_models_list_failed", ids: []string{}, chatIDs: []string{}, imageIDs: []string{}}
	}
	ids := unique(idsFromPayload(models.payload), idsFromPayload(language.payload))
	classifiedImages := []string{}
	classifiedChat := []string{}
	for _, id := range ids {
		if imageModelName.MatchString(id) {
			classifiedImages = append(classifiedImages, id)
		} else {
			classifiedChat = append(classifiedChat, id)
		}
	}
	imageIDs := unique(classifiedImages, idsFromPayload(images.payload))
	chatIDs := []string{}
	for _, id := range classifiedChat {
		if !contains(imageIDs, id) {
			chatIDs = append(chatIDs, id)
		}
	}
	d := discovered{ok: len(chatIDs) > 0, code: "xai_no_chat_models", ids: ids, chatIDs: chatIDs, imageIDs: imageIDs}
	if d.ok {
		d.code = "xai_models_discovered"
	}
	if contains(chatIDs, s.constants.CandidateChatModel) {
		c := s.constants.CandidateChatModel
		d.candidate = &c
	} else if len(chatIDs) > 0 {
		c := chatIDs[0]
		d.candidate = &c
	}
	return d
}
