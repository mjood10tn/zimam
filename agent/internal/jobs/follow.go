package jobs

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log"
	"os"
	"time"

	"zimam/agent/contract"
)

// followPoll is how often Follow checks the job's log and state (§5.7).
var followPoll = 200 * time.Millisecond

// reconcileEvery is how many polls pass between Follow's reconciles, so a
// job whose unit died ends its stream too.
const reconcileEvery = 25

// Follow sends the job's log lines from byte offset from on, until the job
// is final. Then it sends a result line with the job's status, or a
// job_failed error for a failed job (§5.7).
func (m *Manager) Follow(ctx context.Context, id contract.JobID, from int64, send func(any) error) error {
	off := from
	for poll := 1; ; poll++ {
		// The state is read before the log: the runner writes its last line
		// before the final state, so once that state is seen the log is whole.
		st, err := m.readState(id)
		if err != nil {
			return err
		}
		if off, err = m.sendLines(id, off, send); err != nil {
			return err
		}
		if st.final() {
			return send(m.finalLine(st))
		}
		if poll%reconcileEvery == 0 {
			if _, err := m.reconcileJob(ctx, id); err != nil {
				log.Printf("job %s: reconciling: %v", id, err)
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(followPoll):
		}
	}
}

// sendLines sends every complete line of the job's log from off on, and
// returns the offset after the last line sent. A line still being written
// waits for its newline.
func (m *Manager) sendLines(id contract.JobID, off int64, send func(any) error) (int64, error) {
	f, err := os.Open(m.Paths.JobLog(id))
	if errors.Is(err, fs.ErrNotExist) {
		return off, nil // the job hasn't started
	}
	if err != nil {
		return off, err
	}
	defer f.Close()
	if _, err := f.Seek(off, io.SeekStart); err != nil {
		return off, err
	}
	r := bufio.NewReader(f)
	for {
		line, err := r.ReadBytes('\n')
		if errors.Is(err, io.EOF) {
			return off, nil
		}
		if err != nil {
			return off, err
		}
		if err := send(contract.LogLine{Type: "log", Job: id, Offset: off, Line: string(line[:len(line)-1])}); err != nil {
			return off, err
		}
		off += int64(len(line))
	}
}

// finalLine is the line that ends a follow.
func (m *Manager) finalLine(st state) any {
	if st.Status == contract.JobFailed {
		return contract.Error{Type: "error", Code: contract.ErrJobFailed, Message: st.Message}
	}
	data, err := json.Marshal(m.withLogSize(st.JobStatusResult))
	if err != nil {
		return contract.Error{Type: "error", Code: contract.ErrInternal, Message: "Internal error"}
	}
	return contract.Result{Type: "result", OK: true, Data: data}
}
