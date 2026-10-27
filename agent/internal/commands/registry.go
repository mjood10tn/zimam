// Package commands holds the handlers for the commands the agent serves
// (design §5.3).
package commands

import (
	"context"
	"fmt"

	"zimam/agent/contract"
	"zimam/agent/internal/jobs"
)

// Handler answers a command whose params already passed Validate. A
// *contract.AgentError reaches the client as it is; any other error becomes
// internal. Besides a result, a handler may answer with contract.Accepted,
// sent as the accepted line, or with a Stream.
type Handler func(ctx context.Context, p contract.Params) (any, error)

// Stream is a handler's answer that streams lines (job.follow). The server
// runs it after the handler returns, without the direct-command timeout,
// and it sends every line itself, the last one included.
type Stream func(ctx context.Context, send func(any) error) error

// Handlers maps a command name to its handler.
type Handlers map[string]Handler

// New returns the handlers serve installs. Given a job manager, they include
// job.status, job.follow and job.cancel, and a submit handler for every job
// command the manager can run.
func New(m *jobs.Manager) Handlers {
	h := Handlers{
		"agent.version": agentVersion,
	}
	if m == nil {
		return h
	}
	h["job.status"] = jobStatus(m)
	h["job.follow"] = jobFollow(m)
	h["job.cancel"] = jobCancel(m)
	for name := range m.Handlers {
		h[name] = submitJob(m, name)
	}
	return h
}

// JobHandlers returns the job commands' handlers, which run inside each
// job's unit.
func JobHandlers() map[string]jobs.Handler {
	return map[string]jobs.Handler{}
}

// submitJob is a job command's handler: it submits the job and answers
// accepted (§5.7).
func submitJob(m *jobs.Manager, name string) Handler {
	return func(ctx context.Context, p contract.Params) (any, error) {
		d, ok := contract.Lookup(name)
		if !ok {
			return nil, fmt.Errorf("job command %s isn't registered", name)
		}
		id, err := m.Submit(ctx, d, p)
		if err != nil {
			return nil, err
		}
		return contract.Accepted{Type: "accepted", Job: id}, nil
	}
}
