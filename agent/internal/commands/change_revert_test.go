package commands

import (
	"context"
	"errors"
	"testing"

	"zimam/agent/contract"
	"zimam/agent/internal/paths"
	"zimam/agent/internal/sysexec"
)

func TestChangeRevertOfUnknownChangeIsNotFound(t *testing.T) {
	d := sysexec.Deps{Paths: paths.Paths{Root: t.TempDir()}, Run: sysexec.NewFakeRunner()}
	_, err := changeRevert(d)(context.Background(), &contract.ChangeRevertParams{Change: "01KAWZ5V0D3QF2N8R7S6T5V4W3"})
	var ae *contract.AgentError
	if !errors.As(err, &ae) || ae.Code != contract.ErrNotFound {
		t.Errorf("change.revert of a made-up id = %v, want not_found", err)
	}
	if calls := d.Run.(*sysexec.FakeRunner).Calls(); len(calls) != 0 {
		t.Errorf("ran %v", calls)
	}
}
