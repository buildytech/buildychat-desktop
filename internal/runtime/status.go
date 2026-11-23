package runtime

import "context"

// StatusDoc is GET /v1/status: the fields of apps/xai-runtime/src/status.ts the web app reads, plus `capabilities` and
// `host`.
type StatusDoc struct {
	Runtime          string       `json:"runtime"`
	Ready            bool         `json:"ready"`
	LiveModel        bool         `json:"liveModel"`
	Code             string       `json:"code"`
	Key              KeyState     `json:"key"`
	Auth             AuthState    `json:"auth"`
	Models           ModelsState  `json:"models"`
	PublicSecretLeak []string     `json:"publicSecretLeak"`
	Contract         string       `json:"contract"`
	Capabilities     Capabilities `json:"capabilities"`
	Host             Host         `json:"host"`
	Provider         *Provider    `json:"provider,omitempty"`
}

type KeyState struct {
	Present bool    `json:"present"`
	Source  *string `json:"source"`
}

// AuthState: the desktop has no network surface, so no Basic auth; requests come only from its own webview.
type AuthState struct {
	BasicRequired bool `json:"basicRequired"`
	LoopbackOnly  bool `json:"loopbackOnly"`
}

type ModelsState struct {
	Listed            bool     `json:"listed"`
	ChatIDs           []string `json:"chatIds"`
	ImageIDs          []string `json:"imageIds"`
	Candidate         *string  `json:"candidate"`
	ToolCallingProven bool     `json:"toolCallingProven"`
}

func (s *Server) status(ctx context.Context) StatusDoc {
	key, source := "", ""
	if s.keys != nil {
		key, source = s.keys.Key()
	}
	var provider *Provider
	discover := s.discover
	if s.orKeys != nil {
		p := s.provider()
		provider = &p
		if p.SignedIn {
			// Signed in with OpenRouter: its key serves, xAI is not asked (ADR 0067).
			key, source = s.orKeys.Key()
			discover = s.orDiscover
		}
	}
	present := key != ""
	doc := StatusDoc{
		Runtime:          s.constants.Runtime,
		Auth:             AuthState{BasicRequired: false, LoopbackOnly: true},
		Models:           ModelsState{ChatIDs: []string{}, ImageIDs: []string{}},
		PublicSecretLeak: []string{},
		Contract:         s.constants.Contract,
		Capabilities:     s.capabilities,
		Host:             s.host,
		Provider:         provider,
	}
	discoverCode := "xai_api_key_missing"
	if present {
		doc.Key = KeyState{Present: true, Source: &source}
		d := discover(ctx, key)
		doc.Models.Listed = d.ok || len(d.chatIDs) > 0 || len(d.ids) > 0
		doc.Models.ChatIDs = d.chatIDs
		doc.Models.ImageIDs = d.imageIDs
		doc.Models.Candidate = d.candidate
		discoverCode = d.code
	}
	switch {
	case !present:
		doc.Code = "xai_api_key_missing"
	case !doc.Models.Listed:
		doc.Code = discoverCode
	case doc.Models.Candidate == nil:
		doc.Code = "xai_no_chat_models"
	default:
		doc.Code = "xai_runtime_ready"
	}
	doc.Ready = doc.Code == "xai_runtime_ready"
	doc.LiveModel = doc.Ready
	if !present {
		c := s.constants.CandidateChatModel
		doc.Models.Candidate = &c
	}
	return doc
}
