package audit

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"zimam/agent/internal/paths"
	"zimam/agent/internal/sysexec"
)

// newWriter returns a Writer under a temporary root, with the log and locks
// directories in place, as bootstrap leaves them.
func newWriter(t *testing.T) Writer {
	t.Helper()
	p := paths.Paths{Root: t.TempDir()}
	for _, dir := range []string{p.LogDir(), p.LocksDir()} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	return Writer{Paths: p}
}

func entry(params any) Entry {
	return Entry{
		TS:         time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC),
		Actor:      "cli:root",
		Command:    "site.create",
		Params:     params,
		Outcome:    "ok",
		DurationMS: 12,
	}
}

// readLines returns the lines of path, failing unless it ends in a newline.
func readLines(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) == 0 || data[len(data)-1] != '\n' {
		t.Fatalf("audit.log doesn't end in a newline: %q", data)
	}
	return strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
}

func TestEntryIsOneLine(t *testing.T) {
	w := newWriter(t)
	uid := uint32(1001)
	e := entry(Redact(struct {
		Name string `json:"name"`
	}{"line one\nline two\r\n"}))
	e.PeerUID = &uid
	e.Actor = "panel"
	e.RequestID = "r-1"
	e.Outcome = "accepted"
	e.Job = "j-1"
	if err := w.Write(e); err != nil {
		t.Fatalf("Write: %v", err)
	}
	lines := readLines(t, w.Paths.AuditLog())
	if len(lines) != 1 {
		t.Fatalf("got %d lines, want 1: %q", len(lines), lines)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &got); err != nil {
		t.Fatalf("line is not JSON: %v", err)
	}
	keys := slices.Sorted(maps.Keys(got))
	want := []string{"actor", "command", "duration_ms", "job", "outcome", "params", "peer_uid", "request_id", "ts"}
	if !slices.Equal(keys, want) {
		t.Errorf("keys = %q, want %q (§5.8)", keys, want)
	}
	if name := got["params"].(map[string]any)["name"]; name != "line one\nline two\r\n" {
		t.Errorf("params.name = %q", name)
	}
	if got["peer_uid"] != float64(1001) || got["ts"] != "2026-10-27T12:00:00Z" {
		t.Errorf("peer_uid = %v, ts = %v", got["peer_uid"], got["ts"])
	}
}

func TestAppendCreates0600(t *testing.T) {
	w := newWriter(t)
	if err := w.Write(entry(Redact(struct {
		N int `json:"n"`
	}{1}))); err != nil {
		t.Fatalf("first Write: %v", err)
	}
	fi, err := os.Stat(w.Paths.AuditLog())
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("audit.log mode = %v, want 0600", fi.Mode().Perm())
	}
	// Params that didn't decode, from cli:root: both fields are null.
	if err := w.Write(entry(nil)); err != nil {
		t.Fatalf("second Write: %v", err)
	}
	lines := readLines(t, w.Paths.AuditLog())
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2: %q", len(lines), lines)
	}
	if !strings.Contains(lines[0], `"params":{"n":1}`) {
		t.Errorf("first line lost its params: %s", lines[0])
	}
	for _, field := range []string{`"params":null`, `"peer_uid":null`} {
		if !strings.Contains(lines[1], field) {
			t.Errorf("second line lacks %s: %s", field, lines[1])
		}
	}
}

func TestRefusesSymlinkedLog(t *testing.T) {
	outside := t.TempDir()
	cases := map[string]bool{"existing target": true, "dangling target": false}
	for name, exists := range cases {
		t.Run(name, func(t *testing.T) {
			w := newWriter(t)
			target := filepath.Join(outside, strings.ReplaceAll(name, " ", "-"))
			if exists {
				if err := os.WriteFile(target, nil, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.Symlink(target, w.Paths.AuditLog()); err != nil {
				t.Fatal(err)
			}
			if err := w.Write(entry(nil)); err == nil {
				t.Error("Write through a symlinked audit.log succeeded")
			}
			data, err := os.ReadFile(target)
			switch {
			case exists && len(data) != 0:
				t.Errorf("the symlink's target was written: %q", data)
			case !exists && !errors.Is(err, os.ErrNotExist):
				t.Errorf("the symlink's target was created: %v", err)
			}
		})
	}
}

func TestConcurrentWritersKeepLinesWhole(t *testing.T) {
	w := newWriter(t)
	const writers, each = 8, 200
	pad := strings.Repeat("x", 8192) // longer than PIPE_BUF, so interleaving would show
	var wg sync.WaitGroup
	for g := range writers {
		wg.Go(func() {
			w := Writer{Paths: w.Paths}
			for i := range each {
				e := entry(Redact(struct {
					Writer int    `json:"writer"`
					Seq    int    `json:"seq"`
					Pad    string `json:"pad"`
				}{g, i, pad}))
				if err := w.Write(e); err != nil {
					t.Errorf("writer %d, entry %d: %v", g, i, err)
					return
				}
			}
		})
	}
	wg.Wait()
	lines := readLines(t, w.Paths.AuditLog())
	if len(lines) != writers*each {
		t.Fatalf("got %d lines, want %d", len(lines), writers*each)
	}
	seen := map[[2]int]bool{}
	for n, line := range lines {
		var e struct {
			Params struct {
				Writer int    `json:"writer"`
				Seq    int    `json:"seq"`
				Pad    string `json:"pad"`
			} `json:"params"`
		}
		if err := json.Unmarshal([]byte(line), &e); err != nil || e.Params.Pad != pad {
			t.Fatalf("line %d is not a whole entry (%v): %.120q", n+1, err, line)
		}
		seen[[2]int{e.Params.Writer, e.Params.Seq}] = true
	}
	if len(seen) != writers*each {
		t.Errorf("got %d distinct entries, want %d", len(seen), writers*each)
	}
}

func TestWriteWaitsForAuditLock(t *testing.T) {
	w := newWriter(t)
	unlock, err := sysexec.Lock(context.Background(), w.Paths.AuditLock())
	if err != nil {
		t.Fatal(err)
	}
	defer func(old time.Duration) { lockWait = old }(lockWait)
	lockWait = 200 * time.Millisecond
	if err := w.Write(entry(nil)); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Write while audit.lock is held = %v, want context.DeadlineExceeded", err)
	}
	if data, _ := os.ReadFile(w.Paths.AuditLog()); len(data) != 0 {
		t.Errorf("an entry was written without the lock: %q", data)
	}
	unlock()
	if err := w.Write(entry(nil)); err != nil {
		t.Errorf("Write after the lock was released: %v", err)
	}
}
