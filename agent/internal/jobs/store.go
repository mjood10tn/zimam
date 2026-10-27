// Package jobs runs the agent's jobs, each in its own transient systemd
// unit, with its spec, state and log on disk (design §5.7).
package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"time"

	"zimam/agent/contract"
	"zimam/agent/internal/audit"
	"zimam/agent/internal/config"
	"zimam/agent/internal/paths"
	"zimam/agent/internal/sysexec"
)

// Manager submits, runs, reports, follows, cancels and reconciles jobs.
// serve uses one for the job commands, and `zimam-agent job run` one to run
// a job inside its unit.
type Manager struct {
	Paths   paths.Paths
	Systemd Systemd
	Audit   interface{ Write(audit.Entry) error }
	// Handlers holds the job commands' handlers by command name, and
	// Prechecks the prechecks some of them have.
	Handlers  map[string]Handler
	Prechecks map[string]Precheck
	// Config and Run go to job handlers.
	Config config.Agent
	Run    sysexec.Runner
	// LogGID is the group job logs get (zimam-agent, §5.1); -1 leaves it.
	LogGID int
	Now    func() time.Time // nil means time.Now

	lookup func(name string) (contract.Descriptor, bool) // for tests; nil means contract.Lookup
}

func (m *Manager) now() time.Time {
	if m.Now != nil {
		return m.Now().UTC()
	}
	return time.Now().UTC()
}

func (m *Manager) descriptor(name string) (contract.Descriptor, bool) {
	if m.lookup != nil {
		return m.lookup(name)
	}
	return contract.Lookup(name)
}

func (m *Manager) writeAudit(e audit.Entry) {
	if err := m.Audit.Write(e); err != nil {
		log.Printf("audit: %v", err)
	}
}

// spec is spec.json: what the runner needs, secrets included. Only root can
// read it, and it's deleted when the job ends (§5.7).
type spec struct {
	Command string          `json:"command"`
	Subject string          `json:"subject"`
	Params  json.RawMessage `json:"params"`
	Caller  audit.Caller    `json:"caller"`
}

// state is state.json: what job.status reports, and when the job was queued.
type state struct {
	contract.JobStatusResult
	QueuedAt time.Time `json:"queued_at"`
}

func (s state) final() bool {
	switch s.Status {
	case contract.JobSucceeded, contract.JobFailed, contract.JobCancelled:
		return true
	}
	return false
}

func unitName(id contract.JobID) string { return "zimam-job-" + string(id) }

func notFound(id contract.JobID) error {
	return &contract.AgentError{Code: contract.ErrNotFound, Message: fmt.Sprintf("No job %s", id)}
}

func isNotFound(err error) bool {
	var ae *contract.AgentError
	return errors.As(err, &ae) && ae.Code == contract.ErrNotFound
}

// codeOf returns err's protocol code and message: an AgentError's own, or
// internal with err's text.
func codeOf(err error) (contract.ErrorCode, string) {
	var ae *contract.AgentError
	if errors.As(err, &ae) {
		return ae.Code, ae.Message
	}
	return contract.ErrInternal, err.Error()
}

func fail(st *state, code contract.ErrorCode, message string) {
	st.Status = contract.JobFailed
	st.ErrorCode = &code
	st.Message = message
}

func (m *Manager) readState(id contract.JobID) (state, error) {
	if id.Validate() != nil {
		return state{}, notFound(id)
	}
	data, err := os.ReadFile(m.Paths.JobState(id))
	if errors.Is(err, fs.ErrNotExist) {
		return state{}, notFound(id)
	}
	if err != nil {
		return state{}, err
	}
	var st state
	if err := json.Unmarshal(data, &st); err != nil {
		return state{}, fmt.Errorf("%s: %w", m.Paths.JobState(id), err)
	}
	return st, nil
}

func (m *Manager) writeState(id contract.JobID, st state) error {
	data, err := json.Marshal(st)
	if err != nil {
		return err
	}
	return writeFile(m.Paths.JobState(id), append(data, '\n'))
}

func (m *Manager) readSpec(id contract.JobID) (spec, error) {
	data, err := os.ReadFile(m.Paths.JobSpec(id))
	if err != nil {
		return spec{}, err
	}
	var sp spec
	if err := json.Unmarshal(data, &sp); err != nil {
		return spec{}, fmt.Errorf("%s: %w", m.Paths.JobSpec(id), err)
	}
	return sp, nil
}

// writeFile replaces path with data atomically: a temporary file in the same
// directory (mode 0600, as os.CreateTemp makes it), synced, then renamed.
func writeFile(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // fails harmlessly once renamed
	_, err = tmp.Write(data)
	if err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// create writes a new job's directory, spec.json and state.json (queued).
func (m *Manager) create(id contract.JobID, sp spec, now time.Time) error {
	if err := os.MkdirAll(m.Paths.JobsDir(), 0o700); err != nil {
		return err
	}
	if err := os.Mkdir(m.Paths.JobDir(id), 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(sp)
	if err != nil {
		return err
	}
	if err := writeFile(m.Paths.JobSpec(id), append(data, '\n')); err != nil {
		return err
	}
	st := state{QueuedAt: now}
	st.Job, st.Command, st.Subject, st.Status = id, sp.Command, sp.Subject, contract.JobQueued
	return m.writeState(id, st)
}

// lockJob takes the job's lock, which every read-modify-write of its state
// holds.
func (m *Manager) lockJob(ctx context.Context, id contract.JobID) (func(), error) {
	return sysexec.Lock(ctx, filepath.Join(m.Paths.JobDir(id), "lock"))
}

// update changes a job's state under its lock; change returns false to leave
// the state as it is.
func (m *Manager) update(ctx context.Context, id contract.JobID, change func(*state) bool) (state, bool, error) {
	unlock, err := m.lockJob(ctx, id)
	if err != nil {
		return state{}, false, err
	}
	defer unlock()
	st, err := m.readState(id)
	if err != nil || !change(&st) {
		return st, false, err
	}
	return st, true, m.writeState(id, st)
}

// finish gives a job that isn't final yet its final state, set by set, and
// deletes spec.json, whose secrets mustn't outlive the job (§5.7). Every
// path that ends a job goes through it: the runner, Reconcile, Cancel and a
// failed start. It returns the spec as it was, for the audit entry, and
// false when the job was already final.
func (m *Manager) finish(ctx context.Context, id contract.JobID, set func(*state)) (state, *spec, bool, error) {
	unlock, err := m.lockJob(ctx, id)
	if err != nil {
		return state{}, nil, false, err
	}
	defer unlock()
	st, err := m.readState(id)
	if err != nil || st.final() {
		return st, nil, false, err
	}
	set(&st)
	now := m.now()
	st.FinishedAt = &now
	if err := m.writeState(id, st); err != nil {
		return st, nil, false, err
	}
	var sp *spec
	if s, err := m.readSpec(id); err == nil {
		sp = &s
	}
	if err := os.Remove(m.Paths.JobSpec(id)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return st, sp, true, err
	}
	return st, sp, true, nil
}

// auditOutcome writes the entry for a job's final outcome (§5.8), as its
// submitter's. A cancelled job's entry is job.cancel's, which Cancel writes.
func (m *Manager) auditOutcome(st state, sp *spec) {
	e := audit.Entry{TS: m.now(), Command: st.Command, Outcome: "ok", Job: st.Job}
	if st.ErrorCode != nil {
		e.Outcome = "error:" + string(*st.ErrorCode)
	}
	if st.FinishedAt != nil {
		e.DurationMS = st.FinishedAt.Sub(st.QueuedAt).Milliseconds()
	}
	if sp != nil {
		e.PeerUID, e.Actor, e.RequestID = sp.Caller.PeerUID, sp.Caller.Actor, sp.Caller.RequestID
		e.Params = m.redactedParams(*sp)
	}
	m.writeAudit(e)
}

// redactedParams decodes a spec's params for an audit entry, secrets
// redacted, or returns nil when they don't decode.
func (m *Manager) redactedParams(sp spec) any {
	d, ok := m.descriptor(sp.Command)
	if !ok {
		return nil
	}
	p := d.Params()
	if err := contract.DecodeStrict(sp.Params, p); err != nil {
		return nil
	}
	return audit.Redact(p)
}

// jobIDs lists the jobs on disk.
func (m *Manager) jobIDs() ([]contract.JobID, error) {
	entries, err := os.ReadDir(m.Paths.JobsDir())
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var ids []contract.JobID
	for _, e := range entries {
		if id := contract.JobID(e.Name()); e.IsDir() && id.Validate() == nil {
			ids = append(ids, id)
		}
	}
	return ids, nil
}

// withLogSize fills in log_size from the job's log.
func (m *Manager) withLogSize(res contract.JobStatusResult) contract.JobStatusResult {
	if fi, err := os.Stat(m.Paths.JobLog(res.Job)); err == nil {
		res.LogSize = fi.Size()
	}
	return res
}
