package runtime

// Media mode and the ZIP export's pictures (contract cases/media-*.json; apps/xai-runtime/src/media.ts,
// media-asset.ts, media-encode.ts): POST /v1/media generate | result | asset, POST /v1/media?action=encode, and the
// desktop's picture store GET /v1/files/<sha256>.<ext> (capabilities.files). A finished Media request is downloaded
// into the store at once (the xAI URLs expire) and answered as local addresses. Only the generate and result calls carry
// the key; picture downloads never do. Nothing logs a prompt, a URL or a header.

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf16"

	"github.com/buildytech/buildychat-desktop/internal/media"
)

const (
	mediaPath        = "/v1/media"
	filesPrefix      = "/v1/files/"
	maxMediaBody     = 16_000
	mediaTimeout     = 20 * time.Second
	maxImageURL      = 2048
	maxEncodeBase64  = 3_200_000
	failureSnippet   = 240
	unreadableMsg    = "The picture could not be converted; keep the original."
	assetFailMsg     = "The picture could not be read."
	encodeByURLMsg   = "The picture could not be read from that address."
	mediaFailMessage = "The image request could not be completed. No automatic retry was sent."
)

var (
	requestIDRE = regexp.MustCompile(`^[A-Za-z0-9-]{8,80}$`)
	toolsRE     = regexp.MustCompile(`tool|function.?call`)
	refusedRE   = regexp.MustCompile(`not.?support|unavail|invalid|unknown`)
	missingRE   = regexp.MustCompile(`model.?not.?found|unknown.?model|does not exist`)
)

type obj = map[string]any

func mediaJSON(w http.ResponseWriter, status int, body obj) { writeJSON(w, status, body) }

func jsLength(s string) int { return len(utf16.Encode([]rune(s))) }

// classifyFailure mirrors packages/provider-xai/src/errors.ts classifyProviderFailure.
func classifyFailure(status int, snippet string) (string, string) {
	lower := strings.ToLower(snippet)
	switch {
	case status == 401:
		return "provider_unauthorized", "The provider rejected authentication."
	case status == 403:
		return "provider_forbidden", "The provider denied access."
	case status == 429:
		return "rate_limited", "The provider rate limit was reached."
	case status >= 500:
		return "provider_unavailable", "The provider service failed."
	case toolsRE.MatchString(lower) && refusedRE.MatchString(lower):
		return "tools_unsupported", "The model rejected application tools. No other model was substituted."
	case status == 404 || missingRE.MatchString(lower):
		return "model_unavailable", "The requested model is unavailable. No fallback was selected."
	}
	return "provider_failed", "The provider rejected the request."
}

func providerFailure(status int, body []byte) obj {
	if len(body) > failureSnippet {
		body = body[:failureSnippet]
	}
	code, message := classifyFailure(status, string(body))
	return obj{"ok": false, "code": code, "message": message, "status": status}
}

func (s *Server) store() media.Store { return media.Store{Dir: s.mediaDir} }

func (s *Server) media(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("action") == "encode" {
		s.encode(w, r)
		return
	}
	raw, _ := io.ReadAll(io.LimitReader(r.Body, maxMediaBody*4+1))
	var body obj
	text := string(raw)
	if text == "" {
		text = "{}"
	}
	if jsLength(string(raw)) > maxMediaBody || json.Unmarshal([]byte(text), &body) != nil || body == nil {
		mediaJSON(w, http.StatusBadRequest, obj{"ok": false, "code": "invalid_body", "message": "Send one JSON object."})
		return
	}
	if body["action"] == "asset" {
		s.asset(w, r.Context(), body["url"])
		return
	}
	key := ""
	if s.keys != nil {
		key, _ = s.keys.Key()
	}
	if key == "" {
		mediaJSON(w, http.StatusServiceUnavailable, obj{"ok": false, "code": "xai_api_key_missing", "message": "No xAI key is stored on this computer. No provider call was made."})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), mediaTimeout)
	defer cancel()
	started := time.Now()
	var out obj
	var err error
	ok := http.StatusOK
	switch body["action"] {
	case "generate":
		prompt, _ := body["prompt"].(string)
		aspect, _ := body["aspect"].(string)
		if strings.TrimSpace(prompt) == "" || jsLength(prompt) > s.constants.Images.MaxPrompt || !slices.Contains(s.constants.Images.Aspects, aspect) {
			mediaJSON(w, http.StatusUnprocessableEntity, obj{"ok": false, "code": "invalid_request", "message": fmt.Sprintf("A prompt (up to %d characters) and a supported aspect are required.", s.constants.Images.MaxPrompt)})
			return
		}
		n := s.constants.Images.MaxCandidates
		if v, isNum := body["n"].(float64); isNum {
			n = int(math.Max(1, math.Min(float64(n), math.Trunc(v))))
		}
		out, err = s.generate(ctx, key, strings.TrimSpace(prompt), aspect, n)
		ok = http.StatusAccepted
		if err == nil {
			log.Printf("runtime media generate ok=%v n=%d %dms", out["ok"], n, time.Since(started).Milliseconds())
		}
	case "result":
		id, _ := body["requestId"].(string)
		out, err = s.result(ctx, key, id)
		if err == nil && (out["ok"] != true || out["status"] == "done") {
			log.Printf("runtime media result ok=%v code=%v %dms", out["ok"], out["code"], time.Since(started).Milliseconds())
		}
	default:
		mediaJSON(w, http.StatusUnprocessableEntity, obj{"ok": false, "code": "unknown_action", "message": "action is generate, result or asset."})
		return
	}
	if err != nil {
		log.Printf("runtime media failed %dms", time.Since(started).Milliseconds())
		mediaJSON(w, http.StatusBadGateway, obj{"ok": false, "code": "error", "message": mediaFailMessage})
		return
	}
	out["model"] = s.constants.Images.Model
	if out["ok"] != true {
		ok = http.StatusBadGateway
	}
	mediaJSON(w, ok, out)
}

// call sends one keyed provider request and reads at most 1 MB of its answer.
func (s *Server) call(ctx context.Context, method, path, key string, payload any) (int, []byte, error) {
	var body io.Reader
	if payload != nil {
		raw, _ := json.Marshal(payload)
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, s.baseURL()+path, body)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := s.client().Do(req)
	if err != nil {
		return 0, nil, errors.New("provider unreachable")
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return 0, nil, errors.New("provider answer unreadable")
	}
	return res.StatusCode, raw, nil
}

func (s *Server) generate(ctx context.Context, key, prompt, aspect string, n int) (obj, error) {
	status, raw, err := s.call(ctx, http.MethodPost, "/images/generations", key, obj{"model": s.constants.Images.Model, "prompt": prompt, "n": n, "aspect_ratio": aspect, "response_format": "url", "deferred": true})
	if err != nil {
		return nil, err
	}
	if status < 200 || status > 299 {
		return providerFailure(status, raw), nil
	}
	var payload struct {
		RequestID any `json:"request_id"`
	}
	_ = json.Unmarshal(raw, &payload)
	id, _ := payload.RequestID.(string)
	if !requestIDRE.MatchString(id) {
		return obj{"ok": false, "code": "malformed_response", "message": "The image request returned no request id."}, nil
	}
	return obj{"ok": true, "requestId": id}, nil
}

func (s *Server) result(ctx context.Context, key, id string) (obj, error) {
	if !requestIDRE.MatchString(id) {
		return obj{"ok": false, "code": "bad_request_id", "message": "Unknown image request."}, nil
	}
	status, raw, err := s.call(ctx, http.MethodGet, "/images/"+id, key, nil)
	if err != nil {
		return nil, err
	}
	if status == http.StatusAccepted || status == http.StatusNotFound {
		return obj{"ok": true, "status": "pending"}, nil
	}
	if status < 200 || status > 299 {
		return providerFailure(status, raw), nil
	}
	var payload struct {
		Status string `json:"status"`
		Error  struct {
			Code any `json:"code"`
		} `json:"error"`
		Data []struct {
			URL any `json:"url"`
		} `json:"data"`
		Usage struct {
			Cost any `json:"cost_in_usd_ticks"`
		} `json:"usage"`
	}
	_ = json.Unmarshal(raw, &payload)
	switch payload.Status {
	case "pending":
		return obj{"ok": true, "status": "pending"}, nil
	case "failed":
		code := "image_failed"
		if c, isText := payload.Error.Code.(string); isText && c != "" {
			code = "image_" + c
		}
		return obj{"ok": false, "code": code, "message": "The image model could not draw this brief."}, nil
	}
	urls := []string{}
	for _, d := range payload.Data {
		if u, isText := d.URL.(string); isText && strings.HasPrefix(u, "https://") && len(u) <= maxImageURL {
			urls = append(urls, u)
		}
	}
	if len(urls) == 0 {
		return obj{"ok": false, "code": "malformed_response", "message": "The image request finished without pictures."}, nil
	}
	var cost any
	if c, isNum := payload.Usage.Cost.(float64); isNum && !math.IsInf(c, 0) && !math.IsNaN(c) {
		cost = c
	}
	// The desktop keeps every candidate: the URL expires, the stored copy does not. The downloads run side by side, each
	// with its own timeout (not the poll's); a failed download keeps the URL.
	var wg sync.WaitGroup
	for i, u := range urls {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got := s.fetchPicture(context.WithoutCancel(ctx), u, s.constants.Asset.MaxBytes)
			if got.code != "" {
				return
			}
			if name, err := s.store().Put(got.data, got.typ); err == nil {
				urls[i] = media.FilesPrefix + name
			}
		}()
	}
	wg.Wait()
	return obj{"ok": true, "status": "done", "urls": urls, "costUsdTicks": cost}, nil
}

type picture struct {
	data   []byte
	typ    string
	status int
	code   string
}

// allowedAddress is media-asset.ts allowedAssetUrl: https, no credentials or port, an xAI image host.
func (s *Server) allowedAddress(value any) (string, bool) {
	text, isText := value.(string)
	if !isText || jsLength(text) > maxImageURL {
		return "", false
	}
	u, err := url.Parse(text)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Port() != "" || u.Host == "" {
		return "", false
	}
	return u.String(), slices.Contains(s.constants.Asset.Hosts, strings.ToLower(u.Hostname()))
}

func pictureType(header string) string {
	t, _, _ := strings.Cut(header, ";")
	return strings.ToLower(strings.TrimSpace(t))
}

// fetchPicture reads an allowed picture without the key and without following a redirect.
func (s *Server) fetchPicture(ctx context.Context, address string, maxBytes int64) picture {
	ctx, cancel := context.WithTimeout(ctx, mediaTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return picture{status: http.StatusBadGateway, code: "asset_unreachable"}
	}
	req.Header.Set("Accept", "image/*")
	res, err := s.client().Do(req)
	if err != nil {
		return picture{status: http.StatusBadGateway, code: "asset_unreachable"}
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 && res.StatusCode < 400 {
		return picture{status: http.StatusBadGateway, code: "asset_unreachable"}
	}
	if res.StatusCode < 200 || res.StatusCode > 299 {
		status := http.StatusBadGateway
		if res.StatusCode == http.StatusNotFound || res.StatusCode == http.StatusGone {
			status = http.StatusNotFound
		}
		return picture{status: status, code: "asset_upstream_" + strconv.Itoa(res.StatusCode)}
	}
	typ := pictureType(res.Header.Get("content-type"))
	if !slices.Contains(s.constants.Asset.Types, typ) {
		return picture{status: http.StatusUnsupportedMediaType, code: "asset_not_image"}
	}
	declared := res.ContentLength
	if v, err := strconv.ParseInt(res.Header.Get("content-length"), 10, 64); err == nil && v > declared {
		declared = v
	}
	if declared > maxBytes {
		return picture{status: http.StatusRequestEntityTooLarge, code: "asset_too_large"}
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, maxBytes+1))
	if err != nil {
		return picture{status: http.StatusBadGateway, code: "asset_unreachable"}
	}
	if int64(len(data)) > maxBytes {
		return picture{status: http.StatusRequestEntityTooLarge, code: "asset_too_large"}
	}
	return picture{data: data, typ: typ}
}

// readPicture is a stored picture (a /runtime/v1/files address) or an allowed xAI picture.
func (s *Server) readPicture(ctx context.Context, value any, maxBytes int64) picture {
	if text, isText := value.(string); isText && strings.HasPrefix(text, media.FilesPrefix) {
		name := media.NameOfAddress(text)
		if name == "" {
			return picture{status: http.StatusUnprocessableEntity, code: "asset_not_allowed"}
		}
		data, typ, err := s.store().Get(name)
		if err != nil {
			return picture{status: http.StatusNotFound, code: "asset_missing"}
		}
		return picture{data: data, typ: typ}
	}
	address, allowed := s.allowedAddress(value)
	if !allowed {
		return picture{status: http.StatusUnprocessableEntity, code: "asset_not_allowed"}
	}
	return s.fetchPicture(ctx, address, maxBytes)
}

func (s *Server) asset(w http.ResponseWriter, ctx context.Context, value any) {
	got := s.readPicture(ctx, value, s.constants.Asset.MaxBytes)
	if got.code == "asset_not_allowed" {
		mediaJSON(w, got.status, obj{"ok": false, "code": got.code, "message": "Only https pictures of the xAI image hosts are proxied."})
		return
	}
	if got.code != "" {
		mediaJSON(w, got.status, obj{"ok": false, "code": got.code, "message": assetFailMsg})
		return
	}
	w.Header().Set("content-type", got.typ)
	w.Header().Set("content-length", strconv.Itoa(len(got.data)))
	w.Header().Set("cache-control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(got.data)
}

// filter is media-encode.ts readFilter: JS Number() of the query value, the default when absent or not finite,
// clamped to the limits.
func filter(q url.Values, name string, fallback float64, limits [2]float64) float64 {
	v := fallback
	if q.Has(name) {
		text := strings.TrimSpace(q.Get(name))
		if text == "" {
			v = 0
		} else if n, err := strconv.ParseFloat(text, 64); err == nil && !math.IsInf(n, 0) && !math.IsNaN(n) {
			v = n
		}
	}
	return math.Max(limits[0], math.Min(limits[1], v))
}

func (s *Server) encode(w http.ResponseWriter, r *http.Request) {
	e := s.constants.Encode
	q := r.URL.Query()
	var data []byte
	typ := ""
	if q.Has("url") {
		got := s.readPicture(r.Context(), q.Get("url"), e.MaxURLBytes)
		if got.code != "" {
			mediaJSON(w, got.status, obj{"ok": false, "code": got.code, "message": encodeByURLMsg})
			return
		}
		data, typ = got.data, got.typ
	} else {
		typ = pictureType(r.Header.Get("content-type"))
		if !slices.Contains(e.Types, typ) {
			mediaJSON(w, http.StatusUnsupportedMediaType, obj{"ok": false, "code": "encode_not_image", "message": "The body must be the picture itself: jpeg, png, webp, avif or gif."})
			return
		}
		tooLarge := obj{"ok": false, "code": "encode_too_large", "message": fmt.Sprintf("The picture is empty or larger than %d MB.", e.MaxBytes/1_000_000)}
		if r.ContentLength > e.MaxBytes {
			mediaJSON(w, http.StatusRequestEntityTooLarge, tooLarge)
			return
		}
		data, _ = io.ReadAll(io.LimitReader(r.Body, e.MaxBytes+1))
		if len(data) == 0 || int64(len(data)) > e.MaxBytes {
			mediaJSON(w, http.StatusRequestEntityTooLarge, tooLarge)
			return
		}
	}
	f := media.Filters{Contrast: filter(q, "contrast", e.Filters.Contrast, e.FilterLimits.Contrast), Cool: filter(q, "cool", e.Filters.Cool, e.FilterLimits.Cool)}
	img, err := media.Decode(data)
	if err != nil {
		mediaJSON(w, http.StatusUnprocessableEntity, obj{"ok": false, "code": "encode_unreadable", "message": unreadableMsg})
		return
	}
	b := img.Bounds()
	variants := []obj{}
	total := 0
	for _, tier := range media.Plan(b.Dx(), e.Tiers) {
		v, err := media.Encode(img, tier, f)
		if err != nil {
			break
		}
		encoded := base64.StdEncoding.EncodeToString(v.Data)
		if total+len(encoded) > maxEncodeBase64 {
			break
		}
		total += len(encoded)
		variants = append(variants, obj{"width": v.Width, "height": v.Height, "bytes": len(v.Data), "quality": v.Quality, "sharpen": v.Sharpen, "type": "image/webp", "data": encoded})
	}
	if len(variants) == 0 {
		mediaJSON(w, http.StatusUnprocessableEntity, obj{"ok": false, "code": "encode_failed", "message": unreadableMsg})
		return
	}
	mediaJSON(w, http.StatusOK, obj{"ok": true, "original": obj{"width": b.Dx(), "height": b.Dy(), "bytes": len(data), "type": typ}, "filters": f, "variants": variants})
}

// files serves the store read-only: content-addressed names only, immutable, nosniff (set by ServeHTTP).
func (s *Server) files(w http.ResponseWriter, r *http.Request, path, method string) {
	if method != http.MethodGet && method != http.MethodHead {
		w.Header().Set("allow", "GET, HEAD")
		forwardError(w, http.StatusMethodNotAllowed, "method_not_allowed", "The picture store is read-only.")
		return
	}
	data, typ, err := s.store().Get(strings.TrimPrefix(path, filesPrefix))
	if err != nil {
		forwardError(w, http.StatusNotFound, "not_found", "Unknown runtime path.")
		return
	}
	w.Header().Set("content-type", typ)
	w.Header().Set("content-length", strconv.Itoa(len(data)))
	w.Header().Set("cache-control", "public, max-age=31536000, immutable")
	w.WriteHeader(http.StatusOK)
	if method == http.MethodGet {
		_, _ = w.Write(data)
	}
}
