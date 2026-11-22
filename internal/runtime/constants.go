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
		len(c.Encode.Types) == 0 || len(c.Encode.Tiers) == 0 || c.Encode.MaxBytes < 1 || c.Encode.MaxURLBytes < 1 {
		return c, fmt.Errorf("contract constants: incomplete constants.json")
	}
	return c, nil
}
