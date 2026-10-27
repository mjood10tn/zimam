package commands

import (
	"context"

	"zimam/agent/contract"
	"zimam/agent/internal/jobs"
)

// jobFollow streams the job's log from from_offset, then its result (§5.7).
func jobFollow(m *jobs.Manager) Handler {
	return func(_ context.Context, p contract.Params) (any, error) {
		params := p.(*contract.JobFollowParams)
		return Stream(func(ctx context.Context, send func(any) error) error {
			return m.Follow(ctx, params.Job, int64(params.FromOffset), send)
		}), nil
	}
}
