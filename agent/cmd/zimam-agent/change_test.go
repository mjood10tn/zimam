package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"zimam/agent/contract"
	"zimam/agent/internal/audit"
	"zimam/agent/internal/changes"
	"zimam/agent/internal/paths"
	"zimam/agent/internal/sysexec"
)

// The timer's revert is audited as cli:root, whether it worked or not.
func TestRevertCLIIsAudited(t *testing.T) {
	p := paths.Paths{Root: t.TempDir()}
	for _, dir := range []string{p.LogDir(), p.LocksDir(), p.ChangesDir()} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	var fail error
	changes.Register("cli-test", changes.Hooks{Revert: func(context.Context, sysexec.Deps, []byte) error { return fail }})
	ids := []contract.ChangeID{"01JD5Z3W4X6Y7Z8A9B0C1D2E3F", "01JD5Z3W4X6Y7Z8A9B0C1D2E3G"}
	run := sysexec.NewFakeRunner()
	for _, id := range ids {
		record := `{"id":"` + string(id) + `","kind":"cli-test","previous":null,"deadline":"2026-11-24T12:00:00Z"}`
		if err := os.WriteFile(p.ChangeRecord(id), []byte(record), 0o600); err != nil {
			t.Fatal(err)
		}
		run.Add(sysexec.Script{Path: "/usr/bin/systemctl", Args: []string{"stop", "zimam-revert-" + string(id) + ".timer"}})
	}
	d := sysexec.Deps{Paths: p, Run: run, Logf: t.Logf, Now: time.Now}
	var stderr bytes.Buffer
	if code := runChangeRevert(context.Background(), d, audit.Writer{Paths: p}, ids[0], &stderr); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	fail = errors.New("ufw failed")
	if code := runChangeRevert(context.Background(), d, audit.Writer{Paths: p}, ids[1], &stderr); code != 1 {
		t.Errorf("a failed revert exited %d", code)
	}
	entries := readAudit(t, p)
	if len(entries) != 2 {
		t.Fatalf("audit has %d entries, want 2", len(entries))
	}
	for i, want := range []string{"ok", "error:internal"} {
		e := entries[i]
		params, _ := e["params"].(map[string]any)
		if e["actor"] != "cli:root" || e["command"] != "change revert" || e["outcome"] != want || params["change"] != string(ids[i]) {
			t.Errorf("entry %d = %v, want cli:root change revert %s", i, e, want)
		}
	}
}

func TestChangeRevertUsage(t *testing.T) {
	for _, args := range [][]string{nil, {"x"}, {"01JD5Z3W4X6Y7Z8A9B0C1D2E3F", "b"}} {
		var stderr bytes.Buffer
		if code := changeRevert(args, nil, nil, &stderr); code != exitUsage {
			t.Errorf("%q: exit %d, want usage", args, code)
		}
	}
}
