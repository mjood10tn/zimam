package commands

import (
	"context"

	"zimam/agent/contract"
	"zimam/agent/internal/jobs"
)

func jobCancel(m *jobs.Manager) Handler {
	return func(ctx context.Context, p contract.Params) (any, error) {
		return m.Cancel(ctx, p.(*contract.JobCancelParams).Job)
	}
}
