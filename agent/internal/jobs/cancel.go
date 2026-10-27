package jobs

import (
	"context"
	"fmt"

	"zimam/agent/contract"
	"zimam/agent/internal/audit"
)

// Cancel stops a queued or running job's unit, which ends everything in its
// cgroup, and records the job as cancelled (§5.7). A finished job answers
// conflict. Every outcome is audited as job.cancel, with the caller from ctx.
func (m *Manager) Cancel(ctx context.Context, id contract.JobID) (contract.JobCancelResult, error) {
	start := m.now()
	res, err := m.cancel(ctx, id)
	c := audit.CallerFrom(ctx)
	e := audit.Entry{
		TS: start, PeerUID: c.PeerUID, Actor: c.Actor, RequestID: c.RequestID,
		Command: "job.cancel", Params: audit.Redact(contract.JobCancelParams{Job: id}),
		Outcome: "ok", Job: id, DurationMS: m.now().Sub(start).Milliseconds(),
	}
	if err != nil {
		code, _ := codeOf(err)
		e.Outcome = "error:" + string(code)
	}
	m.writeAudit(e)
	return res, err
}

func (m *Manager) cancel(ctx context.Context, id contract.JobID) (contract.JobCancelResult, error) {
	// A unit that died unnoticed fails the job first, so cancelling it
	// answers conflict rather than a failed stop.
	if _, err := m.reconcileJob(ctx, id); err != nil {
		return contract.JobCancelResult{}, err
	}
	st, err := m.readState(id)
	if err != nil {
		return contract.JobCancelResult{}, err
	}
	if st.final() {
		return contract.JobCancelResult{}, finished(id, st.Status)
	}
	unit := unitName(id)
	u, err := m.Systemd.Show(ctx, unit)
	if err != nil {
		return contract.JobCancelResult{}, err
	}
	if u.LoadState != "not-found" { // a queued job's unit may not exist yet
		// systemctl stop returns once the job's processes have exited; the
		// runner leaves the state alone when stopped.
		if err := m.Systemd.Stop(ctx, unit); err != nil {
			return contract.JobCancelResult{}, fmt.Errorf("stopping %s: %w", unit, err)
		}
		m.Systemd.ResetFailed(ctx, unit) // in case it failed on the way down
	}
	st, _, done, err := m.finish(ctx, id, func(st *state) { st.Status = contract.JobCancelled })
	if err != nil {
		return contract.JobCancelResult{}, err
	}
	if !done {
		return contract.JobCancelResult{}, finished(id, st.Status) // it ended on its own meanwhile
	}
	return contract.JobCancelResult{Status: contract.JobCancelled}, nil
}

func finished(id contract.JobID, s contract.JobState) error {
	return &contract.AgentError{Code: contract.ErrConflict, Message: fmt.Sprintf("Job %s has already %s", id, s)}
}
