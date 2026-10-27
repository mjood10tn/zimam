// Package commands holds the handlers for the commands the agent serves
// (design §5.3).
package commands

import (
	"context"

	"zimam/agent/contract"
)

// Handler answers a command whose params already passed Validate. A
// *contract.AgentError reaches the client as it is; any other error becomes
// internal.
type Handler func(ctx context.Context, p contract.Params) (any, error)

// Handlers maps a command name to its handler.
type Handlers map[string]Handler

// New returns the handlers serve installs.
func New() Handlers {
	return Handlers{
		"agent.version": agentVersion,
	}
}
