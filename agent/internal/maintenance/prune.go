package maintenance

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"zimam/agent/contract"
	"zimam/agent/internal/sysexec"
)

// pruneJobs deletes the logs in /var/log/zimam/jobs/ and the state in
// /var/lib/zimam/jobs/ of jobs older than the log retention period (§11.3).
// A job whose state says queued or running stays, however old; a log whose
// state is already gone goes by its own age. It answers how many jobs it
// pruned.
func pruneJobs(d sysexec.Deps) (int, error) {
	before := d.Now().Add(-time.Duration(d.Config.LogRetentionDays) * 24 * time.Hour)
	pruned := map[string]bool{}
	var errs []error
	entries, err := os.ReadDir(d.Paths.JobsDir())
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return 0, err
	}
	for _, de := range entries {
		id := contract.JobID(de.Name())
		if !de.IsDir() || id.Validate() != nil {
			continue
		}
		fi, err := os.Lstat(d.Paths.JobState(id))
		if err != nil || !fi.ModTime().Before(before) || !finished(d.Paths.JobState(id)) {
			continue
		}
		if err := os.RemoveAll(d.Paths.JobDir(id)); err != nil {
			errs = append(errs, err)
			continue
		}
		if err := os.Remove(d.Paths.JobLog(id)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, err)
		}
		pruned[string(id)] = true
	}
	logs, err := os.ReadDir(d.Paths.JobLogsDir())
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return len(pruned), errors.Join(append(errs, err)...)
	}
	for _, de := range logs {
		id, ok := strings.CutSuffix(de.Name(), ".log")
		if !ok || !de.Type().IsRegular() || contract.JobID(id).Validate() != nil || pruned[id] {
			continue
		}
		if _, err := os.Lstat(d.Paths.JobDir(contract.JobID(id))); !errors.Is(err, fs.ErrNotExist) {
			continue // its state is kept, so its log is too
		}
		fi, err := de.Info()
		if err != nil || !fi.ModTime().Before(before) {
			continue
		}
		if err := os.Remove(filepath.Join(d.Paths.JobLogsDir(), de.Name())); err != nil {
			errs = append(errs, err)
			continue
		}
		pruned[id] = true
	}
	if len(pruned) > 0 {
		d.Logf("Pruned %d jobs older than %d days", len(pruned), d.Config.LogRetentionDays)
	}
	return len(pruned), errors.Join(errs...)
}

// finished reports whether a job's state is final; an unreadable state
// counts as not, so it is kept.
func finished(statePath string) bool {
	b, err := os.ReadFile(statePath)
	if err != nil {
		return false
	}
	var st struct {
		Status string `json:"status"`
	}
	if json.Unmarshal(b, &st) != nil {
		return false
	}
	switch contract.JobState(st.Status) {
	case "succeeded", "failed", "cancelled":
		return true
	}
	return false
}

// pruneBackupRuns deletes the run records and logs of backups older than
// the log retention period (R05-D9), by the time their id names. A record
// still running stays, however old; a log whose record is gone goes by its
// own id. The backups themselves are retention's (R05-D8).
func pruneBackupRuns(d sysexec.Deps) error {
	before := d.Now().Add(-time.Duration(d.Config.LogRetentionDays) * 24 * time.Hour)
	old := func(name, suffix string) (contract.BackupID, bool) {
		id, ok := strings.CutSuffix(name, suffix)
		t, err := time.Parse("20060102T150405Z", id)
		return contract.BackupID(id), ok && err == nil && contract.BackupID(id).Validate() == nil && t.Before(before)
	}
	n := 0
	var errs []error
	for _, plan := range planDirs(d.Paths.BackupStateDir(), &errs) {
		records, err := os.ReadDir(d.Paths.BackupRunsDir(plan))
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, err)
		}
		for _, de := range records {
			id, ok := old(de.Name(), ".json")
			if !ok || !de.Type().IsRegular() || !finished(d.Paths.BackupRunRecord(plan, id)) {
				continue
			}
			if err := os.Remove(d.Paths.BackupRunRecord(plan, id)); err != nil {
				errs = append(errs, err)
				continue
			}
			n++
		}
	}
	for _, plan := range planDirs(d.Paths.BackupLogsDir(), &errs) {
		logs, err := os.ReadDir(filepath.Dir(d.Paths.BackupLog(plan, "x")))
		if err != nil {
			errs = append(errs, err)
			continue
		}
		for _, de := range logs {
			id, ok := old(de.Name(), ".log")
			if !ok || !de.Type().IsRegular() {
				continue
			}
			if _, err := os.Lstat(d.Paths.BackupRunRecord(plan, id)); !errors.Is(err, fs.ErrNotExist) {
				continue // its record is kept, so its log is too
			}
			if err := os.Remove(d.Paths.BackupLog(plan, id)); err != nil {
				errs = append(errs, err)
			}
		}
	}
	if n > 0 {
		d.Logf("Pruned %d backup run records older than %d days", n, d.Config.LogRetentionDays)
	}
	return errors.Join(errs...)
}

// planDirs are the folders in dir named like backup plans.
func planDirs(dir string, errs *[]error) []contract.PlanID {
	entries, err := os.ReadDir(dir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		*errs = append(*errs, err)
	}
	var out []contract.PlanID
	for _, de := range entries {
		if de.IsDir() && contract.PlanID(de.Name()).Validate() == nil {
			out = append(out, contract.PlanID(de.Name()))
		}
	}
	return out
}
