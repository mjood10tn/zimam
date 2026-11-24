// Package changes keeps risky server changes only when the owner confirms
// them in time (R06-D1): a change records the previous state, starts a
// transient timer that reverts it, and only change.confirm stops that timer.
package changes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"zimam/agent/contract"
	"zimam/agent/internal/jobs"
	"zimam/agent/internal/paths"
	"zimam/agent/internal/sysexec"
	"zimam/agent/internal/systemd"
)

// Kind names what a change touched, and so which hooks undo or finish it.
type Kind string

const (
	Firewall   Kind = "firewall"
	SSH        Kind = "ssh"
	Cloudflare Kind = "cloudflare"
)

// Hooks are a kind's code paths. Revert restores the previous state the
// change recorded. Confirm, optional, finishes a kept change: an SSH port
// change removes the old port's firewall rule only then (R06-D4).
type Hooks struct {
	Revert  func(ctx context.Context, d sysexec.Deps, previous []byte) error
	Confirm func(ctx context.Context, d sysexec.Deps, previous []byte) error
}

var kinds = map[Kind]Hooks{}

// Register adds a kind's hooks, from the init function of the package that
// makes such changes.
func Register(k Kind, h Hooks) { kinds[k] = h }

// overdueGrace is how long past its deadline a record waits for its own
// timer before maintenance reverts it: the timer fires within a second
// (AccuracySec=1s), so a record still there later lost its timer to a reboot.
const overdueGrace = 30 * time.Second

const systemdRun = "/usr/bin/systemd-run"

var newID = func(now time.Time) contract.ChangeID { return contract.ChangeID(jobs.NewULID(now)) }

// record is /var/lib/zimam/changes/<id>.json.
type record struct {
	ID       contract.ChangeID `json:"id"`
	Kind     Kind              `json:"kind"`
	Previous []byte            `json:"previous"`
	Deadline time.Time         `json:"deadline"`
}

func timerUnit(id contract.ChangeID) string { return "zimam-revert-" + string(id) }

// Begin records a change about to be applied and starts its revert timer.
// The caller applies the change after Begin returns, and calls Revert at
// once if applying fails. Only one change may be pending.
func Begin(ctx context.Context, d sysexec.Deps, k Kind, previous []byte, window time.Duration) (contract.ChangeID, time.Time, error) {
	if _, ok := kinds[k]; !ok {
		return "", time.Time{}, fmt.Errorf("changes: no hooks for kind %q", k)
	}
	unlock, err := sysexec.Lock(ctx, d.Paths.ChangesLock())
	if err != nil {
		return "", time.Time{}, err
	}
	defer unlock()
	ids, err := pending(d.Paths)
	if err != nil {
		return "", time.Time{}, err
	}
	if len(ids) > 0 {
		return "", time.Time{}, &contract.AgentError{Code: contract.ErrBusy, Message: "Another change is waiting for confirmation. Keep it or let it revert first."}
	}
	r := record{ID: newID(d.Now()), Kind: k, Previous: previous, Deadline: d.Now().Add(window).UTC()}
	if err := write(d.Paths.ChangeRecord(r.ID), r); err != nil {
		return "", time.Time{}, err
	}
	_, err = d.Run.Run(ctx, sysexec.Cmd{Path: systemdRun, Args: []string{
		"--unit=" + timerUnit(r.ID), "--on-active=" + strconv.Itoa(int(window.Seconds())) + "s",
		"--timer-property=AccuracySec=1s", "--quiet", "--",
		d.Paths.AgentBinary(), "change", "revert", string(r.ID),
	}})
	if err != nil {
		_ = os.Remove(d.Paths.ChangeRecord(r.ID))
		return "", time.Time{}, fmt.Errorf("starting the revert timer: %w", err)
	}
	return r.ID, r.Deadline, nil
}

// Confirm keeps a change: it stops the revert timer, drops the record and
// runs the kind's confirm hook. Past the deadline the revert is already due,
// so a late confirm is not_found, like one after the revert.
func Confirm(ctx context.Context, d sysexec.Deps, id contract.ChangeID) error {
	unlock, err := sysexec.Lock(ctx, d.Paths.ChangesLock())
	if err != nil {
		return err
	}
	defer unlock()
	r, err := read(d.Paths, id)
	if errors.Is(err, fs.ErrNotExist) || err == nil && d.Now().After(r.Deadline) {
		return &contract.AgentError{Code: contract.ErrNotFound, Message: "This change was already reverted or kept."}
	}
	if err != nil {
		return err
	}
	if err := systemd.Systemctl(ctx, d, "stop", timerUnit(id)+".timer"); err != nil {
		return fmt.Errorf("stopping the revert timer: %w", err)
	}
	if err := os.Remove(d.Paths.ChangeRecord(id)); err != nil {
		return err
	}
	if h := kinds[r.Kind]; h.Confirm != nil {
		return h.Confirm(ctx, d, r.Previous)
	}
	return nil
}

// Revert restores a change's previous state. A change already confirmed or
// reverted is nothing to do. A failed revert keeps the record, so the next
// maintenance run tries again: the owner may be locked out until it works.
func Revert(ctx context.Context, d sysexec.Deps, id contract.ChangeID) error {
	unlock, err := sysexec.Lock(ctx, d.Paths.ChangesLock())
	if err != nil {
		return err
	}
	defer unlock()
	return revertLocked(ctx, d, id)
}

func revertLocked(ctx context.Context, d sysexec.Deps, id contract.ChangeID) error {
	r, err := read(d.Paths, id)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	// The timer is gone when it fired or after a reboot; stopping it is for
	// a revert started early (a failed apply, or maintenance).
	_ = systemd.Systemctl(ctx, d, "stop", timerUnit(id)+".timer")
	h, ok := kinds[r.Kind]
	if !ok {
		return fmt.Errorf("changes: no hooks for kind %q", r.Kind)
	}
	if err := h.Revert(ctx, d, r.Previous); err != nil {
		return fmt.Errorf("reverting change %s (%s): %w", id, r.Kind, err)
	}
	return os.Remove(d.Paths.ChangeRecord(id))
}

// RevertOverdue reverts every change whose timer should have fired by now,
// for maintenance and agent start: a reboot drops transient timers.
func RevertOverdue(ctx context.Context, d sysexec.Deps) ([]contract.ChangeID, error) {
	unlock, err := sysexec.Lock(ctx, d.Paths.ChangesLock())
	if err != nil {
		return nil, err
	}
	defer unlock()
	ids, err := pending(d.Paths)
	if err != nil {
		return nil, err
	}
	var reverted []contract.ChangeID
	var errs []error
	for _, id := range ids {
		r, err := read(d.Paths, id)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if d.Now().Before(r.Deadline.Add(overdueGrace)) {
			continue
		}
		if err := revertLocked(ctx, d, id); err != nil {
			errs = append(errs, err)
			continue
		}
		reverted = append(reverted, id)
	}
	return reverted, errors.Join(errs...)
}

// pending lists the recorded changes, ignoring files that aren't records.
func pending(p paths.Paths) ([]contract.ChangeID, error) {
	entries, err := os.ReadDir(p.ChangesDir())
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var ids []contract.ChangeID
	for _, e := range entries {
		id := contract.ChangeID(strings.TrimSuffix(e.Name(), ".json"))
		if e.Type().IsRegular() && strings.HasSuffix(e.Name(), ".json") && id.Validate() == nil {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	return ids, nil
}

func read(p paths.Paths, id contract.ChangeID) (record, error) {
	var r record
	b, err := os.ReadFile(p.ChangeRecord(id))
	if err != nil {
		return r, err
	}
	if err := json.Unmarshal(b, &r); err != nil {
		return r, fmt.Errorf("change record %s: %w", id, err)
	}
	return r, nil
}

// write saves a record as root 0600, atomically.
func write(path string, r record) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.Marshal(r)
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
