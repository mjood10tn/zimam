package maintenance

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"zimam/agent/internal/helper"
	"zimam/agent/internal/paths"
	"zimam/agent/internal/sysexec"
)

// panelHelper is the panel's storage/logs folder in memory, as the helper
// lists it, with every call recorded.
type panelHelper struct {
	entries []helper.FileInfo
	ops     []string
}

func (h *panelHelper) List(_ context.Context, a helper.Account, dir string) (helper.Listing, error) {
	h.ops = append(h.ops, a.Name+" list "+dir)
	return helper.Listing{Entries: slices.Clone(h.entries)}, nil
}

func (h *panelHelper) RemoveTree(_ context.Context, a helper.Account, name string) error {
	h.ops = append(h.ops, a.Name+" remove-tree "+name)
	return nil
}

const (
	auditOld    = "audit.log-20260101-1767225600.gz"
	auditRecent = "audit.log-20261101-1793491200.gz"
	panelOld    = "laravel.log-20260101-1767225600.gz"
	panelRecent = "laravel.log-20261101-1793491200.gz"
)

const day = 24 * time.Hour

func newRetentionEnv(t *testing.T) (sysexec.Deps, *panelHelper, Homes) {
	t.Helper()
	p := paths.Paths{Root: t.TempDir()}
	if err := os.MkdirAll(p.LogDir(), 0o750); err != nil {
		t.Fatal(err)
	}
	cfg := testConfig(t, p)
	cfg.AuditRetentionDays = 365
	cfg.LogRetentionDays = 14
	h := &panelHelper{}
	d := sysexec.Deps{Paths: p, Run: sysexec.NewFakeRunner(), Config: cfg, Logf: t.Logf, Now: func() time.Time { return capNow }}
	return d, h, Homes{Helper: h, Lookup: func(name string) (helper.Account, error) {
		return helper.Account{Name: name, UID: 990, GID: 990, Home: "/home/" + name}, nil
	}}
}

func writeAudit(t *testing.T, d sysexec.Deps, name string, age time.Duration) string {
	t.Helper()
	path := filepath.Join(d.Paths.LogDir(), name)
	if err := os.WriteFile(path, []byte("x"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, capNow.Add(-age), capNow.Add(-age)); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestSweepDeletesOldAuditArchives(t *testing.T) {
	d, _, homes := newRetentionEnv(t)
	old := writeAudit(t, d, auditOld, 400*day)
	live := writeAudit(t, d, "audit.log", 400*day)
	deleted, err := sweepQuietArchives(context.Background(), d, homes)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if !slices.Equal(deleted, []string{old}) {
		t.Errorf("deleted %q, want %q", deleted, []string{old})
	}
	if _, err := os.Lstat(old); err == nil {
		t.Error("the old archive is still there")
	}
	if _, err := os.Lstat(live); err != nil {
		t.Errorf("the live audit.log went: %v", err)
	}
}

func TestSweepKeepsAuditArchivesInsideRetention(t *testing.T) {
	d, _, homes := newRetentionEnv(t)
	recent := writeAudit(t, d, auditRecent, 364*day)
	deleted, err := sweepQuietArchives(context.Background(), d, homes)
	if err != nil || len(deleted) != 0 {
		t.Fatalf("sweep = %q, %v; want nothing deleted", deleted, err)
	}
	if _, err := os.Lstat(recent); err != nil {
		t.Errorf("a recent archive went: %v", err)
	}
}

func TestSweepIgnoresSymlinkedAuditArchive(t *testing.T) {
	d, _, homes := newRetentionEnv(t)
	target := filepath.Join(t.TempDir(), "precious")
	if err := os.WriteFile(target, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(d.Paths.LogDir(), auditOld)
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	deleted, err := sweepQuietArchives(context.Background(), d, homes)
	if err != nil || len(deleted) != 0 {
		t.Fatalf("sweep = %q, %v; want nothing deleted", deleted, err)
	}
	if _, err := os.Lstat(link); err != nil {
		t.Errorf("the link was removed: %v", err)
	}
	if _, err := os.Stat(target); err != nil {
		t.Errorf("the target went: %v", err)
	}
}

// The panel's archives sit in the zimam user's home: only the helper, as
// zimam, lists and deletes them. Root opens nothing under /home/zimam.
func TestSweepDeletesPanelArchivesThroughHelper(t *testing.T) {
	d, h, homes := newRetentionEnv(t)
	fi := func(name string, age time.Duration, mode os.FileMode) helper.FileInfo {
		return helper.FileInfo{Name: name, Mode: mode, ModTime: capNow.Add(-age)}
	}
	h.entries = []helper.FileInfo{
		fi(panelOld, 30*day, 0o640),
		fi(panelRecent, 3*day, 0o640),
		fi("laravel.log", 30*day, 0o640),
		fi("laravel.log-20260101-1767225600.gz.bak", 30*day, 0o640),
		fi("old.log-20260101-1767225601.gz", 30*day, os.ModeSymlink|0o777),
	}
	deleted, err := sweepQuietArchives(context.Background(), d, homes)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	wantPath := "/home/zimam/panel/shared/storage/logs/" + panelOld
	if !slices.Equal(deleted, []string{wantPath}) {
		t.Errorf("deleted %q, want %q", deleted, []string{wantPath})
	}
	want := []string{
		"zimam list panel/shared/storage/logs",
		"zimam remove-tree panel/shared/storage/logs/" + panelOld,
	}
	if !slices.Equal(h.ops, want) {
		t.Errorf("helper ran\n%s\nwant\n%s", strings.Join(h.ops, "\n"), strings.Join(want, "\n"))
	}
	if _, err := os.Lstat(filepath.Join(d.Paths.Root, "home")); err == nil {
		t.Error("root touched /home")
	}
}

// A run that deletes a quiet archive says so in its audit entry.
func TestMaintenanceAuditsSweptArchives(t *testing.T) {
	e := newEnv(t)
	e.d.Config.AuditRetentionDays = 30
	old := filepath.Join(e.d.Paths.LogDir(), auditOld)
	if err := os.WriteFile(old, []byte("x"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(old, testNow.Add(-90*day), testNow.Add(-90*day)); err != nil {
		t.Fatal(err)
	}
	if err := e.maintain(); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if _, err := os.Lstat(old); err == nil {
		t.Error("the old audit archive is still there")
	}
	if b := readFile(t, e.d.Paths.AuditLog()); !strings.Contains(string(b), auditOld) {
		t.Errorf("the audit entry doesn't name %s:\n%s", auditOld, b)
	}
}
