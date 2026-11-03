package commands

import (
	"context"

	"zimam/agent/contract"
	"zimam/agent/internal/helper"
	"zimam/agent/internal/logs"
	"zimam/agent/internal/sysexec"
)

// logsEnv reads a site's PHP log through the installed agent binary's
// helper, as the job handlers do (§5.6).
func logsEnv(d sysexec.Deps) logs.Env {
	return logs.Env{D: d, Helper: &helper.Client{AgentBinary: d.Paths.AgentBinary()}, Lookup: helper.LookupAccount}
}

// logsList and logsRead answer logs.list and logs.read (§11.2). They are
// routine reads, never audited (§5.8).
func logsList(d sysexec.Deps) Handler {
	return func(ctx context.Context, p contract.Params) (any, error) {
		return logs.List(ctx, logsEnv(d), *p.(*contract.LogsListParams))
	}
}

func logsRead(d sysexec.Deps) Handler {
	return func(ctx context.Context, p contract.Params) (any, error) {
		return logs.Read(ctx, logsEnv(d), *p.(*contract.LogsReadParams))
	}
}
