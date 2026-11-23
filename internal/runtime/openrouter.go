package runtime

// OpenRouter on the desktop (ADR 0067, contract cases/openrouter-*.json, cases/media-openrouter.json): signed in, the
// host serves the chat and the pictures with the user's OpenRouter key, only the contract's x-ai models, and the xAI
// key stays the fallback. The web hosts have none of it.

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"sync"
	"time"

	"github.com/buildytech/buildychat-desktop/internal/media"
)

// openRouterAttribution names the app on OpenRouter (https://openrouter.ai/docs/app-attribution); no user data.
var openRouterAttribution = map[string]string{"HTTP-Referer": "https://github.com/buildytech/buildychat-desktop", "X-Title": "BuildY Chat"}

const (
	// orImageTimeout: one synchronous draw of up to three pictures.
	orImageTimeout = 150 * time.Second
	// maxOrImageAnswer: three base64 pictures at most.
	maxOrImageAnswer = 96 << 20
)

// KeyStore is a key the host can also keep and delete (the OpenRouter sign-in's keychain item).
type KeyStore interface {
	KeySource
	Store(key string) error
	Forget() error
}

// Provider is the optional `provider` of /v1/status (contract/runtime-http/provider.schema.json).
type Provider struct {
	ID       string `json:"id"`
	SignedIn bool   `json:"signedIn"`
	Pending  bool   `json:"pending"`
}

func (s *Server) orKey() string {
	if s.orKeys == nil {
		return ""
	}
	key, _ := s.orKeys.Key()
	return key
}

func (s *Server) orBaseURL() string {
	if s.orBase != "" {
		return s.orBase
	}
	return s.constants.OpenRouter.BaseURL
}

func (s *Server) provider() Provider {
	p := Provider{ID: "xai", Pending: s.signInPending()}
	if s.orKey() != "" {
		p.ID, p.SignedIn = "openrouter", true
	}
	return p
}

func (s *Server) orGetJSON(ctx context.Context, path, key string) listed {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.orBaseURL()+path, nil)
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
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	var payload any
	if json.Unmarshal(raw, &payload) != nil {
		payload = nil
	}
	return listed{status: res.StatusCode, payload: payload}
}

// orDiscover lists OpenRouter's chat and image models and keeps only the contract's: the chat ids in the contract's
// order, the image model when listed, the contract's candidate when listed (else the first chat id).
func (s *Server) orDiscover(ctx context.Context, key string) discovered {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	paths := s.constants.OpenRouter.Discovery
	results := make([]listed, len(paths))
	var wg sync.WaitGroup
	for i, path := range paths {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i] = s.orGetJSON(ctx, path, key)
		}()
	}
	wg.Wait()
	empty := discovered{ids: []string{}, chatIDs: []string{}, imageIDs: []string{}}
	chat, images := results[0], results[1]
	if chat.err != nil {
		empty.code = "xai_models_list_unavailable"
		return empty
	}
	if chat.status != http.StatusOK {
		empty.code = "xai_models_list_failed"
		return empty
	}
	listedChat := idsFromPayload(chat.payload)
	d := discovered{code: "xai_no_chat_models", ids: listedChat, chatIDs: []string{}, imageIDs: []string{}}
	for _, id := range s.constants.OpenRouter.ChatModels {
		if contains(listedChat, id) && id != s.constants.OpenRouter.ImageModel {
			d.chatIDs = append(d.chatIDs, id)
		}
	}
	if images.err == nil && images.status == http.StatusOK && contains(idsFromPayload(images.payload), s.constants.OpenRouter.ImageModel) {
		d.imageIDs = append(d.imageIDs, s.constants.OpenRouter.ImageModel)
	}
	if len(d.chatIDs) > 0 {
		d.ok, d.code = true, "xai_models_discovered"
		c := d.chatIDs[0]
		if contains(d.chatIDs, s.constants.OpenRouter.CandidateChatModel) {
			c = s.constants.OpenRouter.CandidateChatModel
		}
		d.candidate = &c
	}
	return d
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// orGenerate draws with OpenRouter at once (POST /images, base64), stores every picture and answers them done.
func (s *Server) orGenerate(ctx context.Context, key, prompt, aspect string, n int) (obj, error) {
	ctx, cancel := context.WithTimeout(ctx, orImageTimeout)
	defer cancel()
	payload, _ := json.Marshal(obj{"model": s.constants.OpenRouter.ImageModel, "prompt": prompt, "n": n, "aspect_ratio": aspect, "response_format": "b64_json"})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.orBaseURL()+"/images", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	for name, v := range openRouterAttribution {
		req.Header.Set(name, v)
	}
	res, err := s.client().Do(req)
	if err != nil {
		return nil, errors.New("provider unreachable")
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, maxOrImageAnswer+1))
	if err != nil || len(raw) > maxOrImageAnswer {
		return nil, errors.New("provider answer unreadable")
	}
	if res.StatusCode < 200 || res.StatusCode > 299 {
		return providerFailure(res.StatusCode, bytes.ReplaceAll(raw, []byte(key), []byte("[redacted]"))), nil
	}
	var answer struct {
		Data []struct {
			B64 string `json:"b64_json"`
		} `json:"data"`
		Usage struct {
			Cost any `json:"cost"`
		} `json:"usage"`
	}
	_ = json.Unmarshal(raw, &answer)
	urls := []string{}
	for _, d := range answer.Data {
		data, err := base64.StdEncoding.DecodeString(d.B64)
		if err != nil || len(data) == 0 || int64(len(data)) > s.constants.Encode.MaxURLBytes {
			continue
		}
		typ := pictureType(http.DetectContentType(data))
		if name, err := s.store().Put(data, typ); err == nil {
			urls = append(urls, media.FilesPrefix+name)
		}
	}
	if len(urls) == 0 {
		return obj{"ok": false, "code": "malformed_response", "message": "The image request finished without pictures."}, nil
	}
	var cost any
	if c, isNum := answer.Usage.Cost.(float64); isNum && !math.IsInf(c, 0) && !math.IsNaN(c) && c >= 0 {
		cost = int64(math.Round(c * s.constants.OpenRouter.USDTicks))
	}
	return obj{"ok": true, "status": "done", "requestId": "or-" + randomHex(8), "urls": urls, "costUsdTicks": cost}, nil
}
