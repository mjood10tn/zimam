package maintenance

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"zimam/agent/contract"
	"zimam/agent/internal/sysexec"
)

// panelLogsDir is the panel's storage/logs inside the zimam user's home.
const panelLogsDir = "panel/shared/storage/logs"

// panelAccount is the user the panel runs as, and the one the helper
// lists and deletes its archives as.
const panelAccount = "zimam"

// sweepQuietArchives deletes the archives of the logs that rotate by size
// or schedule but whose rotation never runs for lack of a new line: logrotate
// applies maxage only when it rotates the log itself (R01-D5). The audit
// log's archives (/var/log/zimam/audit.log-*.gz, root's) go after
// audit_retention_days, the panel's (storage/logs/*.log-*.gz, the zimam
// user's) after log_retention_days. Root never opens the panel's: the
// helper lists and deletes them as zimam. Only regular files named like
// dateext archives are touched, never links. An archive's age is
// min(mtime, now). It returns the paths it deleted.
func sweepQuietArchives(ctx context.Context, d sysexec.Deps, homes Homes) ([]string, error) {
	var deleted []string
	var errs []error
	now := d.Now()

	auditExpired := now.Add(-time.Duration(d.Config.AuditRetentionDays) * 24 * time.Hour)
	entries, err := os.ReadDir(d.Paths.LogDir())
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		errs = append(errs, fmt.Errorf("listing %s: %w", d.Paths.LogDir(), err))
	}
	for _, de := range entries {
		if !strings.HasPrefix(de.Name(), "audit.log-") || contract.ArchiveName(de.Name()).Validate() != nil || !de.Type().IsRegular() {
			continue
		}
		fi, err := de.Info()
		if err != nil || !fi.ModTime().Before(auditExpired) {
			continue
		}
		p := filepath.Join(d.Paths.LogDir(), de.Name())
		if err := removeRegular(p); err != nil {
			errs = append(errs, fmt.Errorf("deleting %s: %w", p, err))
			continue
		}
		d.Logf("Deleted %s: older than the %d-day audit retention", p, d.Config.AuditRetentionDays)
		deleted = append(deleted, p)
	}

	if homes.Helper == nil || homes.Lookup == nil {
		return deleted, errors.Join(errs...)
	}
	acct, err := homes.Lookup(panelAccount)
	if err != nil {
		return deleted, errors.Join(errs...) // no panel user: nothing of its to age out
	}
	expired := now.Add(-time.Duration(d.Config.LogRetentionDays) * 24 * time.Hour)
	l, err := homes.Helper.List(ctx, acct, panelLogsDir)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, fmt.Errorf("listing the panel's logs: %w", err))
		}
		return deleted, errors.Join(errs...)
	}
	for _, fi := range l.Entries {
		if contract.ArchiveName(fi.Name).Validate() != nil || !fi.Mode.IsRegular() || !fi.ModTime.Before(expired) {
			continue
		}
		rel := path.Join(panelLogsDir, fi.Name)
		where := path.Join(acct.Home, rel)
		if err := homes.Helper.RemoveTree(ctx, acct, rel); err != nil {
			errs = append(errs, fmt.Errorf("deleting %s: %w", where, err))
			continue
		}
		d.Logf("Deleted %s: older than the %d-day retention", where, d.Config.LogRetentionDays)
		deleted = append(deleted, where)
	}
	return deleted, errors.Join(errs...)
}
