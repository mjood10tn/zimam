// Package maintenance is `zimam-agent maintenance`, which
// zimam-maintenance.timer runs every 15 minutes (design §11.3). It settles
// dead jobs, rotates Zimam's logs, counts the pending updates hourly and
// refreshes the sury keyring weekly.
package maintenance

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"slices"
	"time"

	"zimam/agent/contract"
	"zimam/agent/internal/audit"
	"zimam/agent/internal/configtx"
	"zimam/agent/internal/paths"
	"zimam/agent/internal/php"
	"zimam/agent/internal/sysexec"
)

const logrotate = "/usr/sbin/logrotate"

// Jobs is the part of the job manager maintenance uses.
type Jobs interface {
	Reconcile(ctx context.Context) (int, error)
}

// lockWait bounds how long a run waits for another one: a run started by
// hand while the timer's is still going, for example.
var lockWait = 10 * time.Minute

// keyringRefreshEvery is how often the sury keyring is downloaded again
// (D23).
const keyringRefreshEvery = 7 * 24 * time.Hour

// changes is what a run changed, the params of its audit entry. Counting
// updates and rotating logs aren't changes: a run that did only those
// isn't audited (D18).
type changes struct {
	RestoredConfigs  []string `json:"restored_configs,omitempty"` // undone after their process died
	ReconciledJobs   int      `json:"reconciled_jobs"`
	KeyringRefreshed bool     `json:"keyring_refreshed"` // the repository's files changed
}

// Run is one maintenance run, which never overlaps another. Its steps don't
// depend on each other: each runs even when one before it failed, and Run
// returns every failure. A failed keyring refresh is only logged, since the
// installed keyring keeps working. A run that changed something writes one
// audit entry, as cli:root.
func Run(ctx context.Context, d sysexec.Deps, j Jobs, w interface{ Write(audit.Entry) error }) error {
	start := d.Now()
	lockCtx, cancel := context.WithTimeout(ctx, lockWait)
	unlock, err := sysexec.Lock(lockCtx, d.Paths.MaintenanceLock())
	cancel()
	if err != nil {
		return fmt.Errorf("waiting for another maintenance run: %w", err)
	}
	defer unlock()

	var c changes
	var errs []error
	// First, so nothing below runs or reloads against an untested file.
	c.RestoredConfigs, err = configtx.Recover(d.Paths.ConfigTxDir())
	if err != nil {
		errs = append(errs, fmt.Errorf("restoring abandoned config changes: %w", err))
	}
	for _, path := range c.RestoredConfigs {
		d.Logf("Restored %s, left untested by a change whose process died", path)
	}
	c.ReconciledJobs, err = j.Reconcile(ctx)
	if err != nil {
		errs = append(errs, fmt.Errorf("reconciling jobs: %w", err))
	}
	if c.ReconciledJobs > 0 {
		d.Logf("Reconciled %d jobs", c.ReconciledJobs)
	}
	if err := refreshUpdates(ctx, d); err != nil {
		errs = append(errs, err)
	}
	if err := rotateLogs(ctx, d); err != nil {
		errs = append(errs, err)
	}
	c.KeyringRefreshed, err = refreshKeyring(ctx, d)
	if err != nil {
		errs = append(errs, err)
	}
	err = errors.Join(errs...)
	if len(c.RestoredConfigs) == 0 && c.ReconciledJobs == 0 && !c.KeyringRefreshed {
		return err
	}
	outcome := "ok"
	if err != nil {
		outcome = "error:" + string(contract.ErrInternal)
	}
	aerr := w.Write(audit.Entry{
		TS:         start,
		Actor:      "cli:root",
		Command:    "maintenance",
		Params:     audit.Redact(c),
		Outcome:    outcome,
		DurationMS: d.Now().Sub(start).Milliseconds(),
	})
	return errors.Join(err, aerr)
}

// rotateLogs runs logrotate over Zimam's rules with Zimam's own state file,
// never Ubuntu's (§11.3).
func rotateLogs(ctx context.Context, d sysexec.Deps) error {
	_, err := d.Run.Run(ctx, sysexec.Cmd{Path: logrotate, Args: []string{"--state", d.Paths.LogrotateState(), d.Paths.LogrotateConf()}})
	if err != nil {
		return fmt.Errorf("rotating logs: %w", err)
	}
	return nil
}

// refreshRecord is keyring-refresh.json: maintenance's last try at
// refreshing the sury keyring, and why it failed if it did.
type refreshRecord struct {
	AttemptedAt time.Time `json:"attempted_at"`
	Error       string    `json:"error,omitempty"`
}

// refreshKeyring downloads sury's keyring again at most once every 7 days,
// once the repository is set up (D23). sury extends its key's expiry and
// keeps the key, so a server that never refreshed it would stop getting PHP
// updates when the old expiry passed. php.EnsureRepo accepts the download
// only with one primary key whose fingerprint is sury's, and swaps it in
// tested with apt-get update. A failed refresh keeps the installed keyring;
// it is logged and recorded, and next week tries again. refreshKeyring
// reports whether the repository's files changed.
func refreshKeyring(ctx context.Context, d sysexec.Deps) (bool, error) {
	_, err := os.Lstat(d.Paths.SuryKeyring())
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil // bootstrap hasn't set the repository up yet
	}
	if err != nil {
		return false, err
	}
	now := d.Now().UTC()
	if last, ok := lastRefresh(d.Paths); ok && !last.After(now) && now.Sub(last) <= keyringRefreshEvery {
		return false, nil
	}
	before := repoFiles(d.Paths)
	d.Logf("Refreshing the sury keyring")
	rec := refreshRecord{AttemptedAt: now}
	if err := php.EnsureRepo(ctx, d); err != nil {
		d.Logf("Refreshing the sury keyring failed; the installed keyring stays: %v", err)
		rec.Error = err.Error()
	}
	changed := !slices.EqualFunc(before, repoFiles(d.Paths), bytes.Equal)
	if err := writeRootJSON(d.Paths.KeyringRefresh(), rec); err != nil {
		return changed, err
	}
	return changed, nil
}

// lastRefresh reads when maintenance last tried to refresh the keyring. An
// unreadable record counts as none.
func lastRefresh(p paths.Paths) (time.Time, bool) {
	data, err := os.ReadFile(p.KeyringRefresh())
	if err != nil {
		return time.Time{}, false
	}
	var rec refreshRecord
	if err := contract.DecodeStrict(data, &rec); err != nil || rec.AttemptedAt.IsZero() {
		return time.Time{}, false
	}
	return rec.AttemptedAt, true
}

// repoFiles reads the keyring, source and pin; a missing file reads as nil.
func repoFiles(p paths.Paths) [][]byte {
	var files [][]byte
	for _, path := range []string{p.SuryKeyring(), p.SurySources(), p.SuryPin()} {
		b, _ := os.ReadFile(path)
		files = append(files, b)
	}
	return files
}

// writeRootJSON writes one of maintenance's state files, root 0600,
// atomically.
func writeRootJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	noTest := func(context.Context) error { return nil }
	return configtx.Apply(context.Background(), []configtx.Change{
		configtx.RootFile(path, append(data, '\n'), 0o600),
	}, noTest)
}
