package contract

import (
	"errors"
	"slices"
)

func init() {
	Register(Descriptor{
		Name:   "agent.version",
		Kind:   KindRead,
		Params: func() Params { return &AgentVersionParams{} },
		Result: AgentVersionResult{},
	})
}

type AgentVersionParams struct{}

func (AgentVersionParams) Validate() error { return nil }

type AgentVersionResult struct {
	Version   string `json:"version"`
	Protocols []int  `json:"protocols"` // SupportedProtocols
	Build     Build  `json:"build"`
}

// Build is the agent's build flavour: e2e builds carry the test-only
// commands and limits, release builds never do.
type Build string

const (
	BuildRelease Build = "release"
	BuildE2E     Build = "e2e"
)

var builds = []Build{BuildRelease, BuildE2E}

func (b Build) Validate() error {
	if !slices.Contains(builds, b) {
		return errors.New("must be release or e2e")
	}
	return nil
}

func (Build) JSONSchema() Schema {
	return Schema{"type": "string", "enum": builds}
}
