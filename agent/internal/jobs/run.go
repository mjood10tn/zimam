package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"runtime/debug"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"zimam/agent/contract"
	"zimam/agent/internal/config"
	"zimam/agent/internal/helper"
	"zimam/agent/internal/paths"
	"zimam/agent/internal/sysexec"
)

// Handler runs a job command inside the job's unit and returns the command's
// result. A *contract.AgentError becomes the job's error code and message;
// any other error fails the job as internal, with the error's text.
type Handler func(ctx context.Context, j *Context, p contract.Params) (result any, err error)

// Landed is a handler's error for a failure that came after the command's
// effect was in place: a deploy whose release went live before its prune
// failed. The job still fails, with Err's code and message, but the result
// the handler returned with it is stored, so the panel can show what is
// true now instead of what was before.
type Landed struct{ Err error }

func (l *Landed) Error() string { return l.Err.Error() }
func (l *Landed) Unwrap() error { return l.Err }

// recordTimeout bounds recording an outcome after the job's context was
// cancelled; systemd kills the unit not long after it stops it.
const recordTimeout = 10 * time.Second

// Context is what a job handler gets.
type Context struct {
	ID     contract.JobID
	Logf   func(format string, args ...any) // appends "[<RFC3339>] <message>" to the job log
	Config config.Agent
	Paths  paths.Paths
	Run    sysexec.Runner
	Helper *helper.Client
	LogGID int // the group of logs a job writes besides its own (zimam-agent), -1 to leave it
}

// Deps is the job's view of the system, for the packages that take one.
func (j *Context) Deps() sysexec.Deps {
	return sysexec.Deps{Paths: j.Paths, Run: j.Run, Config: j.Config, Logf: j.Logf, Now: time.Now}
}

// RunJob is the body of `zimam-agent job run <id>`, inside the job's unit
// (§5.7). It returns nil once the outcome is recorded, and also when systemd
// stops the unit (ctx cancelled) before the handler's work landed: Cancel
// then records cancelled, and Reconcile records a timeout or a vanished
// unit. A handler that had finished its work when the stop came, or whose
// effect had landed (Landed), is recorded as what it is: the stop waits for
// this process, so the record is there before Cancel looks, and Cancel then
// answers that the job already ended. An error means the outcome couldn't
// be recorded; the unit then fails, and Reconcile records that.
func (m *Manager) RunJob(ctx context.Context, id contract.JobID) error {
	// The unit's RuntimeMaxSec counts from about now; wall clock, not m.Now,
	// which only stamps records.
	start := time.Now()
	sp, err := m.readSpec(id)
	if errors.Is(err, fs.ErrNotExist) {
		return nil // the job ended before it ran: cancelled, or never started
	}
	if err != nil {
		return err
	}
	unlock, err := sysexec.Lock(ctx, m.Paths.SubjectLock(strings.ReplaceAll(sp.Subject, ":", "_")))
	if err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return err
	}
	defer unlock()
	now := m.now()
	_, started, err := m.update(ctx, id, func(st *state) bool {
		if st.Status != contract.JobQueued {
			return false
		}
		st.Status = contract.JobRunning
		st.StartedAt = &now
		return true
	})
	if err != nil || !started {
		return err // not started: it ended while queued
	}
	logFile, err := m.openLog(id)
	if err != nil {
		return m.record(ctx, id, nil, fmt.Errorf("opening the job log: %w", err))
	}
	defer logFile.Close()
	logf := func(format string, args ...any) {
		logFile.Write(logLines(m.now(), fmt.Sprintf(format, args...)))
	}
	// The handler's context ends recordTimeout before systemd kills the
	// unit, so a step without its own bound fails with an outcome recorded
	// here, not killed with none (R04-D12). systemd's kill stays the outer
	// bound.
	hctx := ctx
	l, limited := LimitsFor(sp.Command)
	if limited {
		var cancel context.CancelFunc
		hctx, cancel = context.WithDeadline(ctx, start.Add(time.Duration(l.RuntimeMaxSec)*time.Second-recordTimeout))
		defer cancel()
	}
	result, runErr := m.call(hctx, id, sp, logf)
	if runErr != nil && ctx.Err() == nil && errors.Is(hctx.Err(), context.DeadlineExceeded) {
		logf("Ran out of time: a %s job may run %d s", sp.Command, l.RuntimeMaxSec)
	}
	if ctx.Err() != nil {
		var landed *Landed
		if runErr != nil && !errors.As(runErr, &landed) {
			logf("Stopped")
			return nil
		}
		// What the handler did is done, stop or not: saying "cancelled"
		// would tell the panel nothing changed.
		logf("Stopped, after the work was done")
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(context.WithoutCancel(ctx), recordTimeout)
		defer cancel()
	}
	if runErr != nil {
		_, message := codeOf(runErr)
		logf("Failed: %s", message)
	}
	return m.record(ctx, id, result, runErr)
}

// call decodes the job's params and runs its handler; a panic becomes an
// error.
func (m *Manager) call(ctx context.Context, id contract.JobID, sp spec, logf func(string, ...any)) (result any, err error) {
	d, ok := m.descriptor(sp.Command)
	h := m.Handlers[sp.Command]
	if !ok || h == nil {
		return nil, fmt.Errorf("no job handler for %s", sp.Command)
	}
	p := d.Params()
	if err := contract.DecodeStrict(sp.Params, p); err != nil {
		return nil, fmt.Errorf("decoding the params: %w", err)
	}
	defer func() {
		if r := recover(); r != nil {
			log.Printf("job %s: panic: %v\n%s", id, r, debug.Stack())
			err = fmt.Errorf("panic: %v", r)
		}
	}()
	run := packageGuard{Runner: m.Run, lock: m.Paths.JobCancelLock(id)}
	return h(ctx, &Context{ID: id, Logf: logf, Config: m.Config, Paths: m.Paths, Run: run, Helper: m.Helper, LogGID: m.LogGID}, p)
}

// record writes a run's final state and audits it.
func (m *Manager) record(ctx context.Context, id contract.JobID, result any, runErr error) error {
	var data json.RawMessage
	var landed *Landed
	if runErr == nil || errors.As(runErr, &landed) {
		var err error
		if data, err = json.Marshal(result); err != nil && runErr == nil {
			runErr = fmt.Errorf("encoding the result: %w", err)
		}
	}
	st, sp, done, err := m.finish(ctx, id, func(st *state) {
		if runErr != nil {
			code, message := codeOf(runErr)
			fail(st, code, message)
			if landed != nil {
				st.Result = data // what is in place despite the failure
			}
			return
		}
		st.Status = contract.JobSucceeded
		st.Result = data
	})
	if err != nil {
		return err
	}
	if done {
		m.auditOutcome(st, sp)
	}
	return nil
}

// openLog opens the job's log for appending, mode 0640 and group zimam-agent
// (§5.1), so the panel's user can read it and Nginx can't.
func (m *Manager) openLog(id contract.JobID) (*os.File, error) {
	if err := os.MkdirAll(m.Paths.JobLogsDir(), 0o750); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(m.Paths.JobLog(id), os.O_WRONLY|os.O_APPEND|os.O_CREATE|unix.O_NOFOLLOW, 0o640)
	if err != nil {
		return nil, err
	}
	if err := f.Chmod(0o640); err != nil { // whatever the umask was
		f.Close()
		return nil, err
	}
	if m.LogGID >= 0 {
		if err := f.Chown(-1, m.LogGID); err != nil {
			f.Close()
			return nil, err
		}
	}
	return f, nil
}

// logLines formats a message as job log lines, "[<RFC3339>] <text>", one per
// line of the message, so every line carries its time.
func logLines(now time.Time, message string) []byte {
	ts := now.UTC().Format(time.RFC3339)
	var b []byte
	for line := range strings.SplitSeq(message, "\n") {
		b = fmt.Appendf(b, "[%s] %s\n", ts, line)
	}
	return b
}
