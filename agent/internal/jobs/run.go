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
	"zimam/agent/internal/paths"
	"zimam/agent/internal/sysexec"
)

// Handler runs a job command inside the job's unit and returns the command's
// result. A *contract.AgentError becomes the job's error code and message;
// any other error fails the job as internal, with the error's text.
type Handler func(ctx context.Context, j *Context, p contract.Params) (result any, err error)

// Context is what a job handler gets.
type Context struct {
	ID     contract.JobID
	Logf   func(format string, args ...any) // appends "[<RFC3339>] <message>" to the job log
	Config config.Agent
	Paths  paths.Paths
	Run    sysexec.Runner
}

// RunJob is the body of `zimam-agent job run <id>`, inside the job's unit
// (§5.7). It returns nil once the outcome is recorded, and also when systemd
// stops the unit (ctx cancelled): Cancel then records cancelled, and
// Reconcile records a timeout or a vanished unit. An error means the outcome
// couldn't be recorded; the unit then fails, and Reconcile records that.
func (m *Manager) RunJob(ctx context.Context, id contract.JobID) error {
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
	result, runErr := m.call(ctx, id, sp, logf)
	if ctx.Err() != nil {
		logf("Stopped")
		return nil
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
	return h(ctx, &Context{ID: id, Logf: logf, Config: m.Config, Paths: m.Paths, Run: m.Run}, p)
}

// record writes a run's final state and audits it.
func (m *Manager) record(ctx context.Context, id contract.JobID, result any, runErr error) error {
	var data json.RawMessage
	if runErr == nil {
		var err error
		if data, err = json.Marshal(result); err != nil {
			runErr = fmt.Errorf("encoding the result: %w", err)
		}
	}
	st, sp, done, err := m.finish(ctx, id, func(st *state) {
		if runErr != nil {
			code, message := codeOf(runErr)
			fail(st, code, message)
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
