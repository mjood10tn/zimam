package jobs

import (
	"context"
	"slices"
	"testing"
	"time"

	"zimam/agent/contract"
)

func TestReconcileOOM(t *testing.T)     { reconcileFailedUnit(t, "oom-kill") }
func TestReconcileTimeout(t *testing.T) { reconcileFailedUnit(t, "timeout") }

// reconcileFailedUnit checks a running job whose unit failed with result.
func reconcileFailedUnit(t *testing.T, result string) {
	f := newFixture(t)
	id := f.submit(t, "php.default", &testParams{})
	f.setStatus(t, id, contract.JobRunning)
	f.sd.setUnit(unitName(id), UnitState{LoadState: "loaded", ActiveState: "failed", Result: result})
	if n, err := f.m.Reconcile(context.Background()); n != 1 || err != nil {
		t.Fatalf("Reconcile = %d, %v; want 1", n, err)
	}
	st := f.state(t, id)
	if st.Status != contract.JobFailed || st.Message != result || st.ErrorCode == nil || *st.ErrorCode != contract.ErrInternal || st.FinishedAt == nil {
		t.Errorf("state = %+v, want failed with %s", st, result)
	}
	if !slices.Equal(f.sd.reset, []string{unitName(id)}) {
		t.Errorf("reset-failed %q, want the job's unit", f.sd.reset)
	}
	if exists(f.m.Paths.JobSpec(id)) {
		t.Error("spec.json, with its secrets, outlived the job")
	}
	if e := f.audit.last(t); e.Outcome != "error:internal" || e.Job != id || e.Actor != "user:1" {
		t.Errorf("entry = %+v, want the job's outcome with its submitter", e)
	}
	if n, err := f.m.Reconcile(context.Background()); n != 0 || err != nil {
		t.Errorf("a second Reconcile = %d, %v; want 0", n, err)
	}
}

func TestReconcileUnitGone(t *testing.T) {
	f := newFixture(t)
	running := f.submit(t, "site.provision", &testParams{Name: "shop"})
	f.setStatus(t, running, contract.JobRunning)
	f.sd.setUnit(unitName(running), UnitState{})
	young := f.submit(t, "site.provision", &testParams{Name: "blog"}) // its unit may still be on its way
	f.sd.setUnit(unitName(young), UnitState{})
	if n, err := f.m.Reconcile(context.Background()); n != 1 || err != nil {
		t.Fatalf("Reconcile = %d, %v; want 1", n, err)
	}
	if st := f.state(t, running); st.Status != contract.JobFailed || st.Message != "unit gone" {
		t.Errorf("running job: %+v, want failed with unit gone", st)
	}
	if st := f.state(t, young); st.Status != contract.JobQueued {
		t.Errorf("young queued job: %s, want queued", st.Status)
	}
	if len(f.sd.reset) != 0 {
		t.Errorf("reset-failed %q; no unit failed", f.sd.reset)
	}
}

func TestReconcileNeverStarted(t *testing.T) {
	f := newFixture(t)
	id := f.submit(t, "php.default", &testParams{})
	f.sd.setUnit(unitName(id), UnitState{})
	f.advance(59 * time.Second)
	if n, err := f.m.Reconcile(context.Background()); n != 0 || err != nil {
		t.Fatalf("at 59 s: Reconcile = %d, %v; want 0", n, err)
	}
	f.advance(2 * time.Second)
	if n, err := f.m.Reconcile(context.Background()); n != 1 || err != nil {
		t.Fatalf("at 61 s: Reconcile = %d, %v; want 1", n, err)
	}
	if st := f.state(t, id); st.Status != contract.JobFailed || st.Message != "never started" {
		t.Errorf("state = %+v, want failed with never started", st)
	}
	if exists(f.m.Paths.JobSpec(id)) {
		t.Error("spec.json, with its secrets, outlived the job")
	}
}
