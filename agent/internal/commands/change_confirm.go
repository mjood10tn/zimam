package commands

import (
	"context"

	"zimam/agent/contract"
	"zimam/agent/internal/changes"
	"zimam/agent/internal/sysexec"
)

// changeConfirm keeps a guarded change before its revert timer fires
// (R06-D1, D2). It answers directly: the countdown can't wait in the job
// queue.
func changeConfirm(d sysexec.Deps) Handler {
	return func(ctx context.Context, p contract.Params) (any, error) {
		id := p.(*contract.ChangeConfirmParams).Change
		if err := changes.Confirm(ctx, d, id); err != nil {
			return nil, err
		}
		return contract.ChangeConfirmResult{Change: id}, nil
	}
}
