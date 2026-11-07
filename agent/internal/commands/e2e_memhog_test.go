//go:build e2e

package commands

import (
	"context"
	"testing"

	"zimam/agent/contract"
	"zimam/agent/internal/jobs"
)

func TestE2EMemhogRegistered(t *testing.T) {
	d, ok := contract.Lookup("e2e.memhog")
	if !ok {
		t.Fatal("e2e.memhog isn't registered in an e2e build")
	}
	if d.Kind != contract.KindJob || !d.E2EOnly || !d.Audited {
		t.Fatalf("e2e.memhog is %+v, want an audited, e2e-only job", d)
	}
	if s := d.Subject(&E2EMemhogParams{}); s != "e2e:memhog" {
		t.Fatalf("subject %q, want e2e:memhog", s)
	}
	if err := d.Params().Validate(); err != nil {
		t.Fatalf("empty params refused: %v", err)
	}
	if JobHandlers()["e2e.memhog"] == nil {
		t.Fatal("no job handler for e2e.memhog")
	}
}

// 64M and 60 s, so the 512 MiB it touches ends the unit with oom-kill
// (§14.4's job-limit probe).
func TestE2EMemhogLimits(t *testing.T) {
	l, ok := jobs.LimitsFor("e2e.memhog")
	if want := (jobs.Limits{MemoryMax: "64M", RuntimeMaxSec: 60}); !ok || l != want {
		t.Fatalf("limits %+v (%v), want %+v", l, ok, want)
	}
	if memhogBytes != 512<<20 {
		t.Fatalf("memhogBytes is %d, want 512 MiB", memhogBytes)
	}
}

// The handler writes every page, so the memory is resident rather than
// zero pages the kernel never backs.
func TestE2EMemhogTouchesEveryPage(t *testing.T) {
	defer func(n int) { memhogBytes = n }(memhogBytes)
	memhogBytes = 1 << 20
	var lines []string
	j := &jobs.Context{Logf: func(format string, args ...any) { lines = append(lines, format) }}
	res, err := e2eMemhog(context.Background(), j, &E2EMemhogParams{})
	if err != nil {
		t.Fatal(err)
	}
	r, ok := res.(E2EMemhogResult)
	if !ok || r.Touched != 1<<20/4096 {
		t.Fatalf("result %#v, want %d pages touched", res, 1<<20/4096)
	}
	if len(lines) == 0 {
		t.Fatal("logged nothing")
	}
}
