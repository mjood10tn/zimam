package commands

import (
	"context"

	"zimam/agent/contract"
	"zimam/agent/internal/jobs"
)

func jobStatus(m *jobs.Manager) Handler {
	return func(ctx context.Context, p contract.Params) (any, error) {
		return m.Status(ctx, p.(*contract.JobStatusParams).Job)
	}
}
