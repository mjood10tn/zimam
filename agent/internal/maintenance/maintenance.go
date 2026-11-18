// Package maintenance is `zimam-agent maintenance`, which
// zimam-maintenance.timer runs every 15 minutes (design §11.3). It settles
// dead jobs, rotates Zimam's and every site's logs, counts the pending updates hourly and
// refreshes the sury and NodeSource keyrings weekly and reloads the PHP versions deploys
// marked (spike amendment A3).
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
	"zimam/agent/internal/node"
	"zimam/agent/internal/php"
	"zimam/agent/internal/sysexec"
)

const logrotate = "/usr/sbin/logrotate"

// runBudget bounds the whole run, under the unit's TimeoutStartSec=20min.
// Each step gets its own context from it, so one step that runs out of time
// doesn't cancel the steps after it.
var runBudget = 18 * time.Minute

// stepLimits is the longest each step may take; the run's budget caps them
// all. Rotation has its own: perFileBudget for each rules file, and the whole
// step ends rotationReserve before the run's budget, so the steps after it
// still have time.
type stepLimits struct {
	reconcile, updates, caps, retention, keyring, fpmReload time.Duration
	rotationReserve                                         time.Duration
}

var limits = stepLimits{
	reconcile:       2 * time.Minute,
	updates:         3 * time.Minute,
	caps:            3 * time.Minute,
	retention:       2 * time.Minute,
	keyring:         3 * time.Minute,
	fpmReload:       3 * time.Minute,
	rotationReserve: 5 * time.Minute,
}

// step returns the context of one step: at most limit, never past the run's.
func step(ctx context.Context, limit time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, limit)
}

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
	KeyringRefreshed bool     `json:"keyring_refreshed"`           // the repository's files changed
	DeletedArchives  []string `json:"deleted_archives,omitempty"`  // over a site's log cap (§11.4), or past a retention period
	PrunedJobs       int      `json:"pruned_jobs"`                 // older than the log retention period
	RotationTimeouts []string `json:"rotation_timeouts,omitempty"` // rules files whose logrotate ran out of its budget
	// PHP versions whose FPM a deploy marked, reloaded gracefully (spike
	// amendment A3).
	ReloadedPHP []contract.PHPVersion `json:"reloaded_php,omitempty"`
}

// Run is one maintenance run, which never overlaps another. Its steps don't
// depend on each other: each runs even when one before it failed, and Run
// returns every failure. A failed keyring refresh is only logged, since the
// installed keyring keeps working. A run that changed something writes one
// audit entry, as cli:root.
func Run(ctx context.Context, d sysexec.Deps, j Jobs, homes Homes, w interface{ Write(audit.Entry) error }) error {
	start := d.Now()
	// The budget starts here, so a wait for the lock counts against it: the
	// wait and the run together stay under the unit's TimeoutStartSec.
	runCtx, cancelRun := context.WithTimeout(ctx, runBudget)
	defer cancelRun()
	lockCtx, cancel := context.WithTimeout(runCtx, lockWait)
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
	sctx, cancel := step(runCtx, limits.reconcile)
	c.ReconciledJobs, err = j.Reconcile(sctx)
	cancel()
	if err != nil {
		errs = append(errs, fmt.Errorf("reconciling jobs: %w", err))
	}
	if c.ReconciledJobs > 0 {
		d.Logf("Reconciled %d jobs", c.ReconciledJobs)
	}
	sctx, cancel = step(runCtx, limits.updates)
	err = refreshUpdates(sctx, d)
	cancel()
	if err != nil {
		errs = append(errs, err)
	}
	rctx, cancel := rotationStep(runCtx)
	c.RotationTimeouts, err = rotateEach(rctx, d, perFileBudget)
	cancel()
	if err != nil {
		errs = append(errs, err)
	}
	sctx, cancel = step(runCtx, limits.caps)
	c.DeletedArchives, err = enforceCaps(sctx, d, homes)
	cancel()
	if err != nil {
		errs = append(errs, fmt.Errorf("enforcing the per-site log cap: %w", err))
	}
	sctx, cancel = step(runCtx, limits.retention)
	swept, err := sweepQuietArchives(sctx, d, homes)
	cancel()
	c.DeletedArchives = append(c.DeletedArchives, swept...)
	if err != nil {
		errs = append(errs, fmt.Errorf("applying retention to the quiet logs: %w", err))
	}
	c.PrunedJobs, err = pruneJobs(d)
	if err != nil {
		errs = append(errs, fmt.Errorf("pruning old jobs: %w", err))
	}
	sctx, cancel = step(runCtx, limits.keyring)
	c.KeyringRefreshed, err = refreshKeyring(sctx, d)
	cancel()
	if err != nil {
		errs = append(errs, err)
	}
	sctx, cancel = step(runCtx, limits.fpmReload)
	c.ReloadedPHP, err = php.ReloadMarked(sctx, d)
	cancel()
	if err != nil {
		errs = append(errs, fmt.Errorf("reloading the PHP versions deploys marked: %w", err))
	}
	err = errors.Join(errs...)
	if len(c.RestoredConfigs) == 0 && c.ReconciledJobs == 0 && !c.KeyringRefreshed && len(c.DeletedArchives) == 0 && c.PrunedJobs == 0 && len(c.RotationTimeouts) == 0 && len(c.ReloadedPHP) == 0 {
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

// refreshRecord is keyring-refresh.json: maintenance's last try at
// refreshing a keyring (sury's; node-keyring-refresh.json is NodeSource's),
// and why it failed if it did.
type refreshRecord struct {
	AttemptedAt time.Time `json:"attempted_at"`
	Error       string    `json:"error,omitempty"`
}

// repoRefresh is one APT repository whose keyring maintenance refreshes.
type repoRefresh struct {
	name   string
	files  []string // keyring (it exists once the repository is set up), source, pin
	record string   // the refresh record
	ensure func(context.Context, sysexec.Deps) error
}

// refreshKeyring refreshes sury's keyring and, once Node is installed,
// NodeSource's (R04-D6). One repository's failure never stops the other. It
// reports whether any repository's files changed.
func refreshKeyring(ctx context.Context, d sysexec.Deps) (bool, error) {
	p := d.Paths
	var changed bool
	var errs []error
	for _, r := range []repoRefresh{
		{"sury", []string{p.SuryKeyring(), p.SurySources(), p.SuryPin()}, p.KeyringRefresh(), php.EnsureRepo},
		{"NodeSource", []string{p.NodeKeyring(), p.NodeSources(), p.NodePin()}, p.NodeKeyringRefresh(), node.RefreshRepo},
	} {
		c, err := refreshRepoKeyring(ctx, d, r)
		changed = changed || c
		errs = append(errs, err)
	}
	return changed, errors.Join(errs...)
}

// refreshRepoKeyring downloads a repository's keyring again at most once every
// 7 days, once the repository is set up (D23). sury extends its key's expiry
// and keeps the key, so a server that never refreshed it would stop getting
// updates when the old expiry passed. The repository's EnsureRepo accepts the
// download only with one primary key and the pinned fingerprint, and swaps it
// in tested with apt-get update. A failed refresh keeps the installed
// keyring; it is logged and recorded, and next week tries again. It reports
// whether the repository's files changed.
func refreshRepoKeyring(ctx context.Context, d sysexec.Deps, r repoRefresh) (bool, error) {
	_, err := os.Lstat(r.files[0])
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil // not set up yet
	}
	if err != nil {
		return false, err
	}
	now := d.Now().UTC()
	if last, ok := lastRefresh(r.record); ok && !last.After(now) && now.Sub(last) <= keyringRefreshEvery {
		return false, nil
	}
	before := repoFiles(r.files)
	d.Logf("Refreshing the %s keyring", r.name)
	rec := refreshRecord{AttemptedAt: now}
	if err := r.ensure(ctx, d); err != nil {
		d.Logf("Refreshing the %s keyring failed; the installed keyring stays: %v", r.name, err)
		rec.Error = err.Error()
	}
	changed := !slices.EqualFunc(before, repoFiles(r.files), bytes.Equal)
	if err := writeRootJSON(r.record, rec); err != nil {
		return changed, err
	}
	return changed, nil
}

// lastRefresh reads when maintenance last tried to refresh a keyring. An
// unreadable record counts as none.
func lastRefresh(record string) (time.Time, bool) {
	data, err := os.ReadFile(record)
	if err != nil {
		return time.Time{}, false
	}
	var rec refreshRecord
	if err := contract.DecodeStrict(data, &rec); err != nil || rec.AttemptedAt.IsZero() {
		return time.Time{}, false
	}
	return rec.AttemptedAt, true
}

// repoFiles reads the files; a missing one reads as nil.
func repoFiles(files []string) [][]byte {
	var out [][]byte
	for _, path := range files {
		b, _ := os.ReadFile(path)
		out = append(out, b)
	}
	return out
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
