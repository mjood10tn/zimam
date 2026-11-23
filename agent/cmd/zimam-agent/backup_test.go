package main

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"zimam/agent/internal/audit"
	"zimam/agent/internal/backup"
	"zimam/agent/internal/paths"
	"zimam/agent/internal/sysexec"
)

// A timer's run is audited as cli:root whatever its outcome; a run that
// finds its plan running is skipped with exit 0, and audited too.
func TestCLIRunIsAudited(t *testing.T) {
	p := paths.Paths{Root: t.TempDir()}
	for _, dir := range []string{p.LogDir(), p.LocksDir()} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	d := sysexec.Deps{Paths: p, Run: sysexec.NewFakeRunner(), Logf: t.Logf, Now: time.Now}
	run := func() (int, string) {
		var stderr bytes.Buffer
		code := runBackup(context.Background(), d, backup.RunEnv{LogGID: -1}, audit.Writer{Paths: p}, "nightly", &stderr)
		return code, stderr.String()
	}
	if code, stderr := run(); code != 1 || !strings.Contains(stderr, "nightly doesn't exist") {
		t.Errorf("a missing plan: exit %d, %q", code, stderr)
	}
	unlock, ok, err := sysexec.TryLock(p.BackupLock("nightly"))
	if err != nil || !ok {
		t.Fatal(err)
	}
	defer unlock()
	if code, stderr := run(); code != 0 || !strings.Contains(stderr, "skipped") {
		t.Errorf("a running plan: exit %d, %q", code, stderr)
	}
	entries := readAudit(t, p)
	if len(entries) != 2 {
		t.Fatalf("audit has %d entries, want 2", len(entries))
	}
	for i, want := range []string{"error:not_found", "error:busy"} {
		e := entries[i]
		params, _ := e["params"].(map[string]any)
		if e["actor"] != "cli:root" || e["command"] != "backup run" || e["outcome"] != want || params["plan"] != "nightly" {
			t.Errorf("entry %d = %v, want cli:root backup run nightly %s", i, e, want)
		}
	}
}

func TestBackupRunUsage(t *testing.T) {
	for _, args := range [][]string{nil, {"Nightly"}, {"a", "b"}} {
		var stderr bytes.Buffer
		if code := backupRun(args, nil, nil, &stderr); code != exitUsage {
			t.Errorf("%q: exit %d, want usage", args, code)
		}
	}
}
