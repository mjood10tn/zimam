package jobs

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"zimam/agent/contract"
)

// neverStartedAfter is how long a queued job may wait for its unit before
// Reconcile gives up on it.
const neverStartedAfter = 60 * time.Second

// Reconcile settles every queued or running job whose unit has failed or
// gone (§5.7). serve runs it at start, Status for one job, and maintenance
// every 15 minutes. It returns how many jobs it changed.
func (m *Manager) Reconcile(ctx context.Context) (int, error) {
	ids, err := m.jobIDs()
	if err != nil {
		return 0, err
	}
	changed := 0
	var errs []error
	for _, id := range ids {
		ok, err := m.reconcileJob(ctx, id)
		if err != nil {
			errs = append(errs, fmt.Errorf("job %s: %w", id, err))
		}
		if ok {
			changed++
		}
	}
	return changed, errors.Join(errs...)
}

// reconcileJob settles one job and reports whether it changed. A failed
// unit fails the job with systemd's Result= and is then reset; a running
// job's vanished unit fails it too, and so does a unit that never appeared
// for a job queued over a minute.
func (m *Manager) reconcileJob(ctx context.Context, id contract.JobID) (bool, error) {
	st, err := m.readState(id)
	if isNotFound(err) {
		return false, nil
	}
	if err != nil || st.final() {
		return false, err
	}
	u, err := m.Systemd.Show(ctx, unitName(id))
	if err != nil {
		return false, err
	}
	var reason string
	switch {
	case u.ActiveState == "failed":
		reason = cmp.Or(u.Result, "failed")
	case u.LoadState != "not-found":
		return false, nil // still queued or running
	case st.Status == contract.JobRunning:
		reason = "unit gone"
	case m.now().Sub(st.QueuedAt) > neverStartedAfter:
		reason = "never started"
	default:
		return false, nil // queued; its unit may still be on its way
	}
	// finish checks the state again under the job's lock: the runner may
	// have finished since.
	st, sp, done, err := m.finish(ctx, id, func(st *state) { fail(st, contract.ErrInternal, reason) })
	if err != nil {
		return false, err
	}
	if done {
		m.auditOutcome(st, sp)
	}
	if u.ActiveState == "failed" {
		if err := m.Systemd.ResetFailed(ctx, unitName(id)); err != nil {
			return done, err
		}
	}
	return done, nil
}

// Status reports a job after reconciling it, with log_size read from its log.
func (m *Manager) Status(ctx context.Context, id contract.JobID) (contract.JobStatusResult, error) {
	if _, err := m.reconcileJob(ctx, id); err != nil {
		log.Printf("job %s: reconciling: %v", id, err) // report the stored state anyway
	}
	st, err := m.readState(id)
	if err != nil {
		return contract.JobStatusResult{}, err
	}
	return m.withLogSize(st.JobStatusResult), nil
}
