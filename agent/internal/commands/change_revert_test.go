package commands

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"zimam/agent/contract"
	"zimam/agent/internal/audit"
	"zimam/agent/internal/paths"
	"zimam/agent/internal/sysexec"
)

// change.confirm and change.revert are direct and audited: like job.cancel,
// their handlers write one entry for every outcome, with the caller.
func TestChangeCommandsOfAnUnknownChangeAreNotFoundAndAudited(t *testing.T) {
	p := paths.Paths{Root: t.TempDir()}
	for _, dir := range []string{p.LogDir(), p.LocksDir()} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	d := sysexec.Deps{Paths: p, Run: sysexec.NewFakeRunner()}
	ctx := audit.WithCaller(context.Background(), audit.Caller{Actor: "user:1", RequestID: "01KAWZ5V0D3QF2N8R7S6T5V4W4"})
	const id = "01KAWZ5V0D3QF2N8R7S6T5V4W3"
	for name, h := range map[string]Handler{"change.confirm": changeConfirm(d), "change.revert": changeRevert(d)} {
		var params contract.Params = &contract.ChangeConfirmParams{Change: id}
		if name == "change.revert" {
			params = &contract.ChangeRevertParams{Change: id}
		}
		_, err := h(ctx, params)
		var ae *contract.AgentError
		if !errors.As(err, &ae) || ae.Code != contract.ErrNotFound {
			t.Errorf("%s of a made-up id = %v, want not_found", name, err)
		}
	}
	raw, err := os.ReadFile(p.AuditLog())
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	if len(lines) != 2 {
		t.Fatalf("audit has %d entries, want 2:\n%s", len(lines), raw)
	}
	seen := map[string]bool{}
	for _, l := range lines {
		var e map[string]any
		if err := json.Unmarshal([]byte(l), &e); err != nil {
			t.Fatal(err)
		}
		if e["outcome"] != "error:not_found" || e["actor"] != "user:1" || !strings.Contains(l, id) {
			t.Errorf("entry = %s", l)
		}
		seen[e["command"].(string)] = true
	}
	if !seen["change.confirm"] || !seen["change.revert"] {
		t.Errorf("audited %v", seen)
	}
	if calls := d.Run.(*sysexec.FakeRunner).Calls(); len(calls) != 0 {
		t.Errorf("ran %v", calls)
	}
}
