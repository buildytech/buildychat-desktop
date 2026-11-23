package runtime

import (
	"encoding/json"
	"fmt"
	"io/fs"

	"github.com/buildytech/buildychat-desktop/internal/media"
)

// Constants are the values of contract/runtime-http/constants.json, generated in buildytech/chat from its sources
// (npm run contract:runtime). The desktop host never restates them.
type Constants struct {
	Runtime            string `json:"runtime"`
	Contract           string `json:"contract"`
	CandidateChatModel string `json:"candidateChatModel"`
	XAI                struct {
		DefaultBaseURL string   `json:"defaultBaseUrl"`
		Discovery      []string `json:"discovery"`
	} `json:"xai"`
	Images struct {
		Model         string   `json:"model"`
		Aspects       []string `json:"aspects"`
		MaxCandidates int      `json:"maxCandidates"`
		MaxPrompt     int      `json:"maxPrompt"`
	} `json:"images"`
	Asset struct {
		Hosts    []string `json:"hosts"`
		Types    []string `json:"types"`
		MaxBytes int64    `json:"maxBytes"`
	} `json:"asset"`
	Encode struct {
		Types        []string      `json:"types"`
		MaxBytes     int64         `json:"maxBytes"`
		MaxURLBytes  int64         `json:"maxUrlBytes"`
		Tiers        []media.Tier  `json:"tiers"`
		Filters      media.Filters `json:"filters"`
		FilterLimits struct {
			Contrast [2]float64 `json:"contrast"`
			Cool     [2]float64 `json:"cool"`
		} `json:"filterLimits"`
	} `json:"encode"`
	Export struct {
		MaxBytes     int64  `json:"maxBytes"`
		Name         string `json:"name"`
		FallbackName string `json:"fallbackName"`
	} `json:"export"`
	Host struct {
		Kind       string `json:"kind"`
		DevVersion string `json:"devVersion"`
		Version    string `json:"version"`
	} `json:"host"`
	// OpenRouter is the desktop's second provider (ADR 0067): sign-in, forward and draws.
	OpenRouter struct {
		BaseURL  string `json:"baseUrl"`
		AuthURL  string `json:"authUrl"`
		KeysPath string `json:"keysPath"`
		Callback struct {
			Host       string `json:"host"`
			PathPrefix string `json:"pathPrefix"`
			TTLSeconds int    `json:"ttlSeconds"`
		} `json:"callback"`
		ChatModels         []string `json:"chatModels"`
		CandidateChatModel string   `json:"candidateChatModel"`
		ImageModel         string   `json:"imageModel"`
		Discovery          []string `json:"discovery"`
		SessionHeader      string   `json:"sessionHeader"`
		USDTicks           float64  `json:"usdTicks"`
	} `json:"openrouter"`
}

// LoadConstants reads constants.json from the embedded contract.
func LoadConstants(contract fs.FS) (Constants, error) {
	var c Constants
	raw, err := fs.ReadFile(contract, "constants.json")
	if err != nil {
		return c, fmt.Errorf("contract constants: %w", err)
	}
	if err := json.Unmarshal(raw, &c); err != nil {
		return c, fmt.Errorf("contract constants: %w", err)
	}
	if c.Runtime == "" || c.Contract == "" || c.CandidateChatModel == "" || c.XAI.DefaultBaseURL == "" || len(c.XAI.Discovery) != 3 ||
		c.Images.Model == "" || len(c.Images.Aspects) == 0 || c.Images.MaxCandidates < 1 || c.Images.MaxPrompt < 1 ||
		len(c.Asset.Hosts) == 0 || len(c.Asset.Types) == 0 || c.Asset.MaxBytes < 1 ||
		len(c.Encode.Types) == 0 || len(c.Encode.Tiers) == 0 || c.Encode.MaxBytes < 1 || c.Encode.MaxURLBytes < 1 ||
		c.Export.MaxBytes < 1 || c.Export.Name == "" || c.Export.FallbackName == "" ||
		c.Host.Kind == "" || c.Host.DevVersion == "" || c.Host.Version == "" ||
		c.OpenRouter.BaseURL == "" || c.OpenRouter.AuthURL == "" || c.OpenRouter.KeysPath == "" || c.OpenRouter.Callback.Host == "" ||
		c.OpenRouter.Callback.PathPrefix == "" || c.OpenRouter.Callback.TTLSeconds < 1 || len(c.OpenRouter.ChatModels) == 0 ||
		c.OpenRouter.CandidateChatModel == "" || c.OpenRouter.ImageModel == "" || len(c.OpenRouter.Discovery) != 2 ||
		c.OpenRouter.SessionHeader == "" || c.OpenRouter.USDTicks <= 0 {
		return c, fmt.Errorf("contract constants: incomplete constants.json")
	}
	return c, nil
}
