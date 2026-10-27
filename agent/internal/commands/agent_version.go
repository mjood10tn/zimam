package commands

import (
	"context"
	"slices"

	"zimam/agent/contract"
	"zimam/agent/internal/release"
)

func agentVersion(context.Context, contract.Params) (any, error) {
	return contract.AgentVersionResult{
		Version:   release.Version,
		Protocols: slices.Clone(contract.SupportedProtocols),
		Build:     release.Flavor,
	}, nil
}
