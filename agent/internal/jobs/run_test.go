package jobs

import (
	"context"
	"testing"
	"time"

	"zimam/agent/contract"
)

// The handler's context ends recordTimeout before systemd's RuntimeMaxSec
// kill, so a step without its own bound fails with a recorded outcome
// (R04-D12).
func TestJobContextHasDeadline(t *testing.T) {
	f := newFixture(t)
	var deadline time.Time
	var ok bool
	f.m.Handlers["php.default"] = func(ctx context.Context, _ *Context, _ contract.Params) (any, error) {
		deadline, ok = ctx.Deadline()
		return testResult{}, nil
	}
	id := f.submit(t, "php.default", &testParams{})
	before := time.Now()
	f.run(t, id)
	after := time.Now()
	l, _ := LimitsFor("php.default")
	budget := time.Duration(l.RuntimeMaxSec)*time.Second - recordTimeout
	if !ok || deadline.Before(before.Add(budget)) || deadline.After(after.Add(budget)) {
		t.Errorf("deadline = %v (set %t), want start + %v", deadline, ok, budget)
	}
}

// A handler that runs out of time is recorded as failed, not left running.
func TestJobPastDeadlineIsRecordedFailed(t *testing.T) {
	f := newFixture(t)
	f.m.Handlers["php.default"] = func(ctx context.Context, _ *Context, _ contract.Params) (any, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	old := limits["php.default"]
	limits["php.default"] = Limits{MemoryMax: "256M", RuntimeMaxSec: 10} // budget 0: the deadline is now
	defer func() { limits["php.default"] = old }()
	id := f.submit(t, "php.default", &testParams{})
	f.run(t, id)
	if st := f.state(t, id); st.Status != contract.JobFailed {
		t.Errorf("state = %+v, want failed", st)
	}
}
