//go:build e2e

package commands

import (
	"context"
	"testing"
	"time"

	"zimam/agent/contract"
	"zimam/agent/internal/jobs"
)

func TestE2ESleepRegistered(t *testing.T) {
	d, ok := contract.Lookup("e2e.sleep")
	if !ok {
		t.Fatal("e2e.sleep isn't registered in an e2e build")
	}
	if d.Kind != contract.KindJob || !d.E2EOnly || !d.Audited {
		t.Fatalf("e2e.sleep is %+v, want an audited, e2e-only job", d)
	}
	if s := d.Subject(&E2ESleepParams{Seconds: 2, Subject: "contract-life"}); s != "e2e:contract-life" {
		t.Fatalf("subject %q, want e2e:contract-life", s)
	}
	if JobHandlers()["e2e.sleep"] == nil {
		t.Fatal("no job handler for e2e.sleep")
	}
}

func TestE2ESleepLimits(t *testing.T) {
	l, ok := jobs.LimitsFor("e2e.sleep")
	if want := (jobs.Limits{MemoryMax: "64M", RuntimeMaxSec: 180}); !ok || l != want {
		t.Fatalf("limits %+v (%v), want %+v (D22)", l, ok, want)
	}
}

// The subject becomes part of a lock file name under /run/zimam/locks/.
func TestE2ESleepRejectsPathSubject(t *testing.T) {
	for _, s := range []contract.SiteName{"../x", "a/b", "", "Upper"} {
		if err := (&E2ESleepParams{Seconds: 1, Subject: s}).Validate(); err == nil {
			t.Errorf("subject %q accepted", s)
		}
	}
}

func TestE2ESleepSecondsRange(t *testing.T) {
	for seconds, ok := range map[int]bool{0: false, 1: true, 120: true, 121: false, -1: false} {
		err := (&E2ESleepParams{Seconds: seconds, Subject: "contract-life"}).Validate()
		if (err == nil) != ok {
			t.Errorf("seconds %d: Validate() = %v", seconds, err)
		}
	}
}

func TestE2ESleepSleepsAndLogs(t *testing.T) {
	var lines []string
	j := &jobs.Context{Logf: func(format string, args ...any) { lines = append(lines, format) }}
	start := time.Now()
	res, err := e2eSleep(context.Background(), j, &E2ESleepParams{Seconds: 1, Subject: "contract-life"})
	if err != nil {
		t.Fatal(err)
	}
	if took := time.Since(start); took < time.Second {
		t.Fatalf("returned after %v, want at least 1 s", took)
	}
	if r, ok := res.(E2ESleepResult); !ok || r.Slept != 1 {
		t.Fatalf("result %#v, want slept 1", res)
	}
	if len(lines) == 0 {
		t.Fatal("logged nothing; the contract's lifecycle scenario follows the log")
	}
}

func TestE2ESleepStopsWhenCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(100*time.Millisecond, cancel)
	j := &jobs.Context{Logf: func(string, ...any) {}}
	start := time.Now()
	_, err := e2eSleep(ctx, j, &E2ESleepParams{Seconds: 30, Subject: "contract-cancel"})
	if err == nil || time.Since(start) > 5*time.Second {
		t.Fatalf("after %v: err %v, want the context's error at once", time.Since(start), err)
	}
}
