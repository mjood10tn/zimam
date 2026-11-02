package jobs

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"zimam/agent/contract"
	"zimam/agent/internal/audit"
	"zimam/agent/internal/sysexec"
)

// Precheck runs before a job is created. It answers conflict, not_found or
// validation_failed synchronously (used from milestone 2 on).
type Precheck func(ctx context.Context, p contract.Params) error

// maxActive is how many jobs may be queued or running at once (§5.7).
const maxActive = 2

// Submit starts a job for d (§5.7). Under submit.lock it refuses with busy
// while the job's subject has a queued or running job or 2 jobs are, runs
// the precheck, writes the accepted audit entry, writes spec.json and
// state.json, and starts the job's unit. The caller comes from ctx; every
// outcome is audited, and a job whose accepted entry can't be written is
// refused before it exists (§5.8: a change that can't be audited doesn't
// happen).
func (m *Manager) Submit(ctx context.Context, d contract.Descriptor, p contract.Params) (contract.JobID, error) {
	start := m.now()
	c := audit.CallerFrom(ctx)
	entry := func(id contract.JobID, outcome string) audit.Entry {
		return audit.Entry{
			TS: start, PeerUID: c.PeerUID, Actor: c.Actor, RequestID: c.RequestID,
			Command: d.Name, Params: audit.Redact(p), Outcome: outcome, Job: id,
			DurationMS: m.now().Sub(start).Milliseconds(),
		}
	}
	accepted := func(id contract.JobID) error {
		return m.Audit.Write(entry(id, "accepted"))
	}
	id, err := m.submit(ctx, d, p, start, accepted)
	if err != nil {
		code, _ := codeOf(err)
		m.writeAudit(entry(id, "error:"+string(code)))
	}
	return id, err
}

func (m *Manager) submit(ctx context.Context, d contract.Descriptor, p contract.Params, now time.Time, accepted func(contract.JobID) error) (contract.JobID, error) {
	lim, ok := LimitsFor(d.Name)
	if !ok || d.Kind != contract.KindJob || d.Subject == nil || m.Handlers[d.Name] == nil {
		return "", fmt.Errorf("jobs: %s isn't a job command with limits and a handler", d.Name)
	}
	subject := d.Subject(p)
	params, err := json.Marshal(p)
	if err != nil {
		return "", err
	}
	unlock, err := sysexec.Lock(ctx, m.Paths.SubmitLock())
	if err != nil {
		return "", err
	}
	defer unlock()
	active, err := m.activeJobs()
	if err != nil {
		return "", err
	}
	for _, st := range active {
		if st.Subject == subject {
			return "", busy(fmt.Sprintf("A job for %s is already queued or running", subject))
		}
	}
	if len(active) >= maxActive {
		return "", busy(fmt.Sprintf("%d jobs are already queued or running", len(active)))
	}
	if pre := m.Prechecks[d.Name]; pre != nil {
		if err := pre(ctx, p); err != nil {
			return "", err
		}
	}
	id := newJobID(now)
	if err := accepted(id); err != nil {
		log.Printf("audit: %v", err)
		return "", &contract.AgentError{Code: contract.ErrInternal,
			Message: "The audit log can't be written, so the job wasn't started. Check that /var/log/zimam is writable and has free space."}
	}
	sp := spec{Command: d.Name, Subject: subject, Params: params, Caller: audit.CallerFrom(ctx)}
	if err := m.create(id, sp, now); err != nil {
		return "", err
	}
	argv := []string{m.Paths.AgentBinary(), "job", "run", string(id)}
	if err := m.Systemd.StartTransient(ctx, unitName(id), lim.properties(), argv); err != nil {
		err = fmt.Errorf("starting %s: %w", unitName(id), err)
		if _, _, _, ferr := m.finish(ctx, id, func(st *state) { fail(st, contract.ErrInternal, err.Error()) }); ferr != nil {
			log.Printf("job %s: recording the failed start: %v", id, ferr)
		}
		return id, err
	}
	return id, nil
}

func busy(message string) error {
	return &contract.AgentError{Code: contract.ErrBusy, Message: message}
}

// activeJobs returns the jobs that are queued or running.
func (m *Manager) activeJobs() ([]state, error) {
	ids, err := m.jobIDs()
	if err != nil {
		return nil, err
	}
	var active []state
	for _, id := range ids {
		st, err := m.readState(id)
		if isNotFound(err) {
			continue // a job directory without its state
		}
		if err != nil {
			return nil, err
		}
		if !st.final() {
			active = append(active, st)
		}
	}
	return active, nil
}

const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// newJobID returns a ULID: 48 bits of Unix milliseconds, then 80 random
// bits, in Crockford base32, so job ids sort by creation time.
func newJobID(now time.Time) contract.JobID {
	var b [16]byte
	binary.BigEndian.PutUint64(b[:8], uint64(now.UnixMilli())<<16)
	rand.Read(b[6:]) // crypto/rand never fails
	hi, lo := binary.BigEndian.Uint64(b[:8]), binary.BigEndian.Uint64(b[8:])
	var out [26]byte
	for i := len(out) - 1; i >= 0; i-- {
		out[i] = crockford[lo&31]
		lo = lo>>5 | hi<<59
		hi >>= 5
	}
	return contract.JobID(out[:])
}
