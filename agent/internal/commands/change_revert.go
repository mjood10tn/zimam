package commands

import (
	"context"
	"errors"
	"io/fs"
	"os"

	"zimam/agent/contract"
	"zimam/agent/internal/changes"
	"zimam/agent/internal/sysexec"
)

// changeRevert undoes a guarded change at once, when the owner presses
// Revert now (R06-D2). A change already kept or reverted is not_found, as
// for change.confirm.
func changeRevert(d sysexec.Deps) Handler {
	return func(ctx context.Context, p contract.Params) (any, error) {
		id := p.(*contract.ChangeRevertParams).Change
		if _, err := os.Stat(d.Paths.ChangeRecord(id)); errors.Is(err, fs.ErrNotExist) {
			return nil, &contract.AgentError{Code: contract.ErrNotFound, Message: "This change was already reverted or kept."}
		}
		if err := changes.Revert(ctx, d, id); err != nil {
			return nil, err
		}
		return contract.ChangeRevertResult{Change: id}, nil
	}
}
