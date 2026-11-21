package runtime

import (
	"encoding/json"
	"fmt"
	"io/fs"
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
		Model string `json:"model"`
	} `json:"images"`
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
	if c.Runtime == "" || c.Contract == "" || c.CandidateChatModel == "" || c.XAI.DefaultBaseURL == "" || len(c.XAI.Discovery) != 3 {
		return c, fmt.Errorf("contract constants: incomplete constants.json")
	}
	return c, nil
}
