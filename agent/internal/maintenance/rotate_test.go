package maintenance

import (
	"context"
	"errors"
	"fmt"
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

// writeRules plants rules files in logrotate.d, standing in for bootstrap's
// and the sites' own.
func writeRules(t *testing.T, p paths.Paths, names ...string) {
	t.Helper()
	if err := os.MkdirAll(p.LogrotateDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, n := range names {
		if err := os.WriteFile(filepath.Join(p.LogrotateDir(), n), []byte("# rules\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// rotateRunner answers each logrotate run by the rules file it was given:
// a file in block waits for its context to end.
type rotateRunner struct {
	mu    sync.Mutex
	calls []sysexec.Cmd
	block map[string]bool
}

func (r *rotateRunner) Run(ctx context.Context, c sysexec.Cmd) (sysexec.Result, error) {
	r.mu.Lock()
	r.calls = append(r.calls, c)
	r.mu.Unlock()
	if r.block[c.Args[len(c.Args)-1]] {
		<-ctx.Done()
		return sysexec.Result{}, ctx.Err()
	}
	return sysexec.Result{}, nil
}

func (r *rotateRunner) files() []string {
	var out []string
	for _, c := range r.calls {
		out = append(out, c.Args[len(c.Args)-1])
	}
	return out
}

func rotateDeps(t *testing.T, run sysexec.Runner) sysexec.Deps {
	t.Helper()
	p := paths.Paths{Root: t.TempDir()}
	writeRules(t, p, "zimam-own.conf", "site-b.conf", "site-a.conf", "other.conf")
	return sysexec.Deps{Paths: p, Run: run, Logf: t.Logf, Now: func() time.Time { return testNow }}
}

func TestRotateEachRunsOneLogrotatePerFile(t *testing.T) {
	r := &rotateRunner{}
	d := rotateDeps(t, r)
	timedOut, err := rotateEach(context.Background(), d, time.Minute)
	if err != nil || len(timedOut) != 0 {
		t.Fatalf("rotateEach = %v, %v", timedOut, err)
	}
	dir := d.Paths.LogrotateDir()
	want := []string{filepath.Join(dir, "zimam-own.conf"), filepath.Join(dir, "site-a.conf"), filepath.Join(dir, "site-b.conf")}
	if got := r.files(); !slices.Equal(got, want) {
		t.Errorf("ran logrotate over %q, want %q", got, want)
	}
	for _, c := range r.calls {
		if c.Path != logrotate || !c.KillGroup {
			t.Errorf("call %+v: want %s killing its whole group", c, logrotate)
		}
	}
}

func TestRotateEachUsesTheSharedState(t *testing.T) {
	r := &rotateRunner{}
	d := rotateDeps(t, r)
	if _, err := rotateEach(context.Background(), d, time.Minute); err != nil {
		t.Fatal(err)
	}
	if len(r.calls) != 3 {
		t.Fatalf("%d runs, want 3", len(r.calls))
	}
	for _, c := range r.calls {
		if len(c.Args) != 3 || c.Args[0] != "--state" || c.Args[1] != d.Paths.LogrotateState() {
			t.Errorf("args %q, want --state %s <file>", c.Args, d.Paths.LogrotateState())
		}
	}
}

func TestRotateTimeoutMovesOnToNextSite(t *testing.T) {
	d := rotateDeps(t, nil)
	a := filepath.Join(d.Paths.LogrotateDir(), "site-a.conf")
	r := &rotateRunner{block: map[string]bool{a: true}}
	d.Run = r
	var logs []string
	d.Logf = func(f string, args ...any) { logs = append(logs, strings.TrimSpace(fmt.Sprintf(f, args...))) }
	timedOut, err := rotateEach(context.Background(), d, 100*time.Millisecond)
	if err != nil {
		t.Fatalf("rotateEach: %v", err)
	}
	if !slices.Equal(timedOut, []string{a}) {
		t.Errorf("timedOut = %q, want [%s]", timedOut, a)
	}
	b := filepath.Join(d.Paths.LogrotateDir(), "site-b.conf")
	if got := r.files(); got[len(got)-1] != b {
		t.Errorf("ran %q, want site-b.conf to run after site-a.conf timed out", got)
	}
	want := "Rotating " + a + " took longer than 100ms; the next file continues"
	if !slices.Contains(logs, want) {
		t.Errorf("logged %q, want %q", logs, want)
	}
}

func TestRotateEachStopsWhenTheRunIsCancelled(t *testing.T) {
	r := &rotateRunner{}
	d := rotateDeps(t, r)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	timedOut, err := rotateEach(ctx, d, time.Minute)
	if !errors.Is(err, context.Canceled) || len(timedOut) != 0 || len(r.calls) != 0 {
		t.Errorf("rotateEach = %v, %v after %d runs; want it to stop at once", timedOut, err, len(r.calls))
	}
}
