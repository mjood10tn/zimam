package backup

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"

	"zimam/agent/contract"
	"zimam/agent/internal/helper"
	"zimam/agent/internal/paths"
	"zimam/agent/internal/sysexec"
)

// RunEnv is what a run needs besides Deps: the helper for the files and
// panel parts, the account lookup, and the run logs' group (zimam-agent,
// or -1 to leave it).
type RunEnv struct {
	Helper Helper
	Lookup func(username string) (helper.Account, error)
	LogGID int
}

// RunPlan is one run of a plan, for backup.run (trigger panel) and
// `zimam-agent backup run` (trigger timer). It holds the plan's lock, so a
// second run answers busy. It uploads every part, then the manifest, and
// prunes by retention; a failed run deletes what it uploaded. Its record
// and log say how it went (R05-D5, D8, D9).
func RunPlan(ctx context.Context, d sysexec.Deps, env RunEnv, id contract.PlanID, trigger contract.BackupTrigger) (contract.BackupRunResult, error) {
	res := contract.BackupRunResult{Plan: id, Parts: []contract.BackupPartInfo{}}
	release, err := planBusy(d.Paths, id)
	if err != nil {
		return res, err
	}
	defer release()
	plans, err := loadPlans(d.Paths)
	if err != nil {
		return res, err
	}
	i := findPlan(plans, id)
	if i < 0 {
		return res, planNotFound(id)
	}
	plan := plans[i]
	if len(plan.Sites) == 0 && len(plan.Databases) == 0 && !plan.Panel {
		return res, &contract.AgentError{Code: contract.ErrConflict, Message: fmt.Sprintf("backup plan %s has nothing left to back up", id)}
	}
	if err := failStaleRecords(d, id); err != nil {
		return res, err
	}
	started := d.Now().UTC()
	res.Backup = contract.BackupID(started.Format(idLayout))
	if _, err := os.Lstat(d.Paths.BackupRunRecord(id, res.Backup)); !errors.Is(err, fs.ErrNotExist) {
		return res, fmt.Errorf("backup %s of %s already has a record: is the clock right?", res.Backup, id)
	}
	logFile, err := openRunLog(d.Paths, id, res.Backup, env.LogGID)
	if err != nil {
		return res, err
	}
	defer logFile.Close()
	jobLog := d.Logf
	d.Logf = func(format string, args ...any) {
		jobLog(format, args...)
		fmt.Fprintf(logFile, "[%s] %s\n", time.Now().UTC().Format(time.RFC3339), fmt.Sprintf(format, args...))
	}
	rec := contract.BackupRunRecord{Plan: id, Backup: res.Backup, Trigger: trigger, Status: contract.BackupRunning,
		Parts: []contract.BackupPartInfo{}, StartedAt: started}
	if err := saveRecord(d.Paths, rec); err != nil {
		return res, err
	}
	d.Logf("Backup %s of plan %s started (%s)", res.Backup, id, trigger)
	uploaded, err := runParts(ctx, d, env, plan, res.Backup, &rec)
	if err == nil {
		err = WriteManifest(ctx, d, plan.Destination, id, res.Backup, started, rec.Parts)
	}
	finished := d.Now().UTC()
	rec.FinishedAt = &finished
	if err != nil {
		if uploaded {
			purgeBackup(ctx, d, plan.Destination, id, res.Backup)
		}
		rec.Status, rec.Error = contract.BackupFailed, err.Error()
		d.Logf("Backup %s failed: %v", res.Backup, err)
		if serr := saveRecord(d.Paths, rec); serr != nil {
			err = fmt.Errorf("%w; saving its record: %v", err, serr)
		}
		return res, err
	}
	rec.Status = contract.BackupSucceeded
	if err := saveRecord(d.Paths, rec); err != nil {
		return res, err
	}
	res.Parts = rec.Parts
	d.Logf("Backup %s is complete: %d parts", res.Backup, len(rec.Parts))
	// The backup is whole whatever pruning does; a failure is only logged,
	// and the next run tries again.
	if err := Prune(ctx, d, plan.Destination, id, plan.Retention); err != nil {
		d.Logf("Pruning by retention failed: %v", err)
	}
	return res, nil
}

// runParts uploads the plan's databases, then its sites' files, then the
// panel's database, adding each part to rec as it passes. It stops at the
// first failure; uploaded says whether anything may be on the destination.
// The id must be new there: a clock set back must not mix two backups.
func runParts(ctx context.Context, d sysexec.Deps, env RunEnv, plan contract.BackupPlanParams, id contract.BackupID, rec *contract.BackupRunRecord) (uploaded bool, err error) {
	found, err := listBackups(ctx, d, plan.Destination, plan.ID)
	if err != nil {
		return false, err
	}
	if _, ok := found[id]; ok {
		return false, fmt.Errorf("the destination already has backup %s of %s: is the clock right?", id, plan.ID)
	}
	dir := backupDir(plan.Destination, plan.ID, id)
	pipe := func(src Source) error {
		uploaded = true
		info, err := Pipe(ctx, d, src, dir+ObjectName(src.Part))
		if err != nil {
			return err
		}
		rec.Parts = append(rec.Parts, info)
		return saveRecord(d.Paths, *rec)
	}
	for _, db := range plan.Databases {
		if err := pipe(DatabaseSource(d, db)); err != nil {
			return uploaded, err
		}
	}
	for _, site := range plan.Sites {
		a, err := env.Lookup("s_" + string(site))
		if err != nil {
			return uploaded, fmt.Errorf("files-%s: %w", site, err)
		}
		files, err := SiteFiles(ctx, env.Helper, a)
		if err != nil {
			return uploaded, fmt.Errorf("files-%s: %w", site, err)
		}
		if err := pipe(ArchiveSource(env.Helper, a, contract.BackupPart("files-"+site), files...)); err != nil {
			return uploaded, err
		}
	}
	if plan.Panel {
		a, err := env.Lookup("zimam")
		if err != nil {
			return uploaded, fmt.Errorf("panel: %w", err)
		}
		if err := pipe(PanelSource(d, env.Helper, a)); err != nil {
			return uploaded, err
		}
	}
	return uploaded, nil
}

// purgeBackup deletes a failed backup's folder, even after the run was
// cancelled. Retention deletes it a day later if this fails.
func purgeBackup(ctx context.Context, d sysexec.Deps, dest contract.DestinationID, plan contract.PlanID, id contract.BackupID) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
	defer cancel()
	if _, err := rcloneRun(ctx, d, nil, "purge", string(dest)+":"+string(plan)+"/"+string(id)); err != nil {
		d.Logf("Could not delete the failed backup %s (retention deletes it after a day): %v", id, err)
		return
	}
	d.Logf("Deleted the failed backup %s", id)
}

// failStaleRecords marks failed the plan's records still saying running:
// the caller holds the plan's lock, so their runs died (killed, or the
// server stopped).
func failStaleRecords(d sysexec.Deps, plan contract.PlanID) error {
	rs, err := planRecords(d.Paths, plan)
	if err != nil {
		return err
	}
	for _, r := range rs {
		if r.Status != contract.BackupRunning {
			continue
		}
		r.Status, r.Error = contract.BackupFailed, "the run stopped before it finished"
		if err := saveRecord(d.Paths, r); err != nil {
			return err
		}
	}
	return nil
}

// openRunLog creates a run's log, root:zimam-agent 0640 like a job log, in
// folders the group can read (R05-D9).
func openRunLog(p paths.Paths, plan contract.PlanID, id contract.BackupID, gid int) (*os.File, error) {
	path := p.BackupLog(plan, id)
	for _, dir := range []string{p.BackupLogsDir(), filepath.Dir(path)} {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return nil, err
		}
		if gid >= 0 {
			if err := os.Chown(dir, -1, gid); err != nil {
				return nil, err
			}
		}
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE|unix.O_NOFOLLOW, 0o640)
	if err != nil {
		return nil, err
	}
	err = f.Chmod(0o640) // whatever the umask was
	if err == nil && gid >= 0 {
		err = f.Chown(-1, gid)
	}
	if err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}
