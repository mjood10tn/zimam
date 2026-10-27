// Package audit writes /var/log/zimam/audit.log (§5.8): one JSON line per
// entry, with secrets redacted from params.
package audit

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"

	"golang.org/x/sys/unix"

	"zimam/agent/contract"
	"zimam/agent/internal/paths"
	"zimam/agent/internal/sysexec"
)

// Entry is one line of /var/log/zimam/audit.log (§5.8).
type Entry struct {
	TS         time.Time      `json:"ts"`
	PeerUID    *uint32        `json:"peer_uid"` // nil for cli:root
	Actor      string         `json:"actor"`
	RequestID  string         `json:"request_id,omitempty"`
	Command    string         `json:"command"`
	Params     any            `json:"params"`  // always passed through Redact; nil when params didn't decode
	Outcome    string         `json:"outcome"` // accepted | ok | error:<code>
	Job        contract.JobID `json:"job,omitempty"`
	DurationMS int64          `json:"duration_ms"`
}

// lockWait bounds how long Write waits for audit.lock. Write takes no
// context, and a stuck lock holder mustn't hang the caller.
var lockWait = 5 * time.Second

// writeMu queues this process's writers, so they don't poll audit.lock
// against each other; audit.lock serialises separate processes.
var writeMu sync.Mutex

// Writer appends entries to Paths.AuditLog(). It doesn't create the log or
// locks directories.
type Writer struct {
	Paths paths.Paths
}

// Write appends e as one JSON line. It opens the log for each entry, so
// rotation needs no signal, and refuses a symlinked log. e.Params is written
// as given: callers pass it through Redact.
func (w Writer) Write(e Entry) error {
	line, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("audit: %w", err)
	}
	line = append(line, '\n')

	writeMu.Lock()
	defer writeMu.Unlock()
	f, err := os.OpenFile(w.Paths.AuditLog(), os.O_WRONLY|os.O_APPEND|os.O_CREATE|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return fmt.Errorf("audit: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), lockWait)
	defer cancel()
	unlock, err := sysexec.Lock(ctx, w.Paths.AuditLock())
	if err != nil {
		f.Close()
		return fmt.Errorf("audit: locking %s: %w", w.Paths.AuditLock(), err)
	}
	defer unlock()
	_, err = f.Write(line)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return fmt.Errorf("audit: %w", err)
	}
	return nil
}
