package jobs

import (
	"context"
	"encoding/json"
	"slices"
	"testing"

	"zimam/agent/contract"
)

// The runner and Cancel hand over as they do under systemd: Stop sends
// SIGTERM and waits for the runner to exit, and the runner leaves the
// state to Cancel.
func TestCancelRunning(t *testing.T) {
	f := newFixture(t)
	started := make(chan struct{})
	f.m.Handlers["php.default"] = func(ctx context.Context, _ *Context, _ contract.Params) (any, error) {
		close(started)
		<-ctx.Done() // until systemd stops the unit
		return nil, ctx.Err()
	}
	id := f.submit(t, "php.default", &testParams{})
	runCtx, sigterm := context.WithCancel(context.Background())
	runErr := make(chan error, 1)
	go func() { runErr <- f.m.RunJob(runCtx, id) }()
	<-started
	var runResult error
	var statusAtExit contract.JobState
	f.sd.onStop = func(string) {
		sigterm()
		runResult = <-runErr
		statusAtExit = f.state(t, id).Status
	}
	res, err := f.m.Cancel(callerCtx, id)
	if err != nil || res.Status != contract.JobCancelled {
		t.Fatalf("Cancel = %+v, %v; want cancelled", res, err)
	}
	if runResult != nil || statusAtExit != contract.JobRunning {
		t.Errorf("the runner returned %v and left %s; want nil and running, for Cancel to settle", runResult, statusAtExit)
	}
	if st := f.state(t, id); st.Status != contract.JobCancelled || st.FinishedAt == nil {
		t.Errorf("state = %+v, want cancelled", st)
	}
	if exists(f.m.Paths.JobSpec(id)) {
		t.Error("spec.json, with its secrets, outlived the job")
	}
	if all := f.audit.all(); len(all) != 2 || all[1].Command != "job.cancel" || all[1].Outcome != "ok" {
		t.Errorf("audit = %+v, want accepted, then one job.cancel ok", all)
	}
	if !slices.Equal(f.sd.stopped, []string{unitName(id)}) {
		t.Errorf("stopped %q, want the job's unit", f.sd.stopped)
	}
}

func TestCancelFinishedConflict(t *testing.T) {
	f := newFixture(t)
	id := f.submit(t, "php.default", &testParams{})
	f.run(t, id)
	_, err := f.m.Cancel(callerCtx, id)
	expectCode(t, err, contract.ErrConflict)
	if len(f.sd.stopped) != 0 {
		t.Errorf("stopped %q for a finished job", f.sd.stopped)
	}
	if e := f.audit.last(t); e.Command != "job.cancel" || e.Outcome != "error:conflict" {
		t.Errorf("entry = %+v, want job.cancel error:conflict", e)
	}
	_, err = f.m.Cancel(callerCtx, "01JBX7Q3A1B2C3D4E5F6G7H8J9")
	expectCode(t, err, contract.ErrNotFound)
}

// A unit that died unnoticed fails the job first, so cancelling it answers
// conflict without a stop.
func TestCancelDeadUnit(t *testing.T) {
	f := newFixture(t)
	id := f.submit(t, "php.default", &testParams{})
	f.setStatus(t, id, contract.JobRunning)
	f.sd.setUnit(unitName(id), UnitState{LoadState: "loaded", ActiveState: "failed", Result: "oom-kill"})
	_, err := f.m.Cancel(callerCtx, id)
	expectCode(t, err, contract.ErrConflict)
	if st := f.state(t, id); st.Status != contract.JobFailed || st.Message != "oom-kill" {
		t.Errorf("state = %+v, want failed with oom-kill", st)
	}
	if len(f.sd.stopped) != 0 {
		t.Errorf("stopped %q, a dead unit", f.sd.stopped)
	}
}

func TestCancelAudited(t *testing.T) {
	f := newFixture(t)
	id := f.submit(t, "site.provision", &testParams{Name: "shop", Password: "hunter2"})
	if _, err := f.m.Cancel(callerCtx, id); err != nil {
		t.Fatal(err)
	}
	e := f.audit.last(t)
	if e.Command != "job.cancel" || e.Outcome != "ok" || e.Job != id || e.Actor != "user:1" ||
		e.RequestID != testRequestID || e.PeerUID == nil || *e.PeerUID != testUID {
		t.Errorf("entry = %+v, want job.cancel ok with the caller", e)
	}
	if b, _ := json.Marshal(e.Params); string(b) != `{"job":"`+string(id)+`"}` {
		t.Errorf("params = %s, want the job", b)
	}
	if exists(f.m.Paths.JobSpec(id)) {
		t.Error("spec.json, with its secrets, outlived the job")
	}
}
