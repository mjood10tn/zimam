package mysql

import (
	"context"
	"strings"
	"testing"
	"time"

	"zimam/agent/internal/paths"
	"zimam/agent/internal/sysexec"
)

// deadlineRunner records the time left on each call's context.
type deadlineRunner struct {
	left  []time.Duration
	stdin []string
	err   error
}

func (r *deadlineRunner) Run(ctx context.Context, c sysexec.Cmd) (sysexec.Result, error) {
	if dl, ok := ctx.Deadline(); ok {
		r.left = append(r.left, time.Until(dl))
	} else {
		r.left = append(r.left, -1)
	}
	b := new(strings.Builder)
	if c.Stdin != nil {
		buf := make([]byte, 256)
		n, _ := c.Stdin.Read(buf)
		b.Write(buf[:n])
	}
	r.stdin = append(r.stdin, b.String())
	return sysexec.Result{}, r.err
}

// Ping runs SELECT 1 and is bounded by its own 10 s, not the statement limit
// of 30 s, because callers budget for it.
func TestPingHasItsOwnBound(t *testing.T) {
	r := &deadlineRunner{}
	d := sysexec.Deps{Paths: paths.Paths{Root: t.TempDir()}, Run: r, Logf: t.Logf, Now: time.Now}
	if err := Ping(context.Background(), d); err != nil {
		t.Fatal(err)
	}
	if len(r.left) != 1 || r.left[0] <= 0 || r.left[0] > pingTimeout {
		t.Errorf("time left on the call: %v, want at most %s", r.left, pingTimeout)
	}
	if got := strings.TrimSpace(r.stdin[0]); got != "SELECT 1;" {
		t.Errorf("sent %q", got)
	}
	if pingTimeout >= queryTimeout {
		t.Errorf("the ping's bound %s is not tighter than a statement's %s", pingTimeout, queryTimeout)
	}
}
