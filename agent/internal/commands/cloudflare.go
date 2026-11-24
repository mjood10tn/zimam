package commands

import (
	"context"

	"zimam/agent/contract"
	"zimam/agent/internal/cloudflare"
	"zimam/agent/internal/jobs"
)

func init() {
	// The realip file, and the origin restriction as a guarded change
	// (R06-D7, D9).
	jobHandlers["cloudflare.mode"] = func(ctx context.Context, j *jobs.Context, p contract.Params) (any, error) {
		return cloudflare.Mode(ctx, j.Deps(), *p.(*contract.CloudflareModeParams))
	}
}
