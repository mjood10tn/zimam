package backup

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"slices"
	"time"

	"zimam/agent/contract"
	"zimam/agent/internal/paths"
	"zimam/agent/internal/sites"
	"zimam/agent/internal/sysexec"
	"zimam/agent/internal/systemd"
)

// plansFile is the plans' state: each plan as backup.plan set it.
type plansFile struct {
	Plans []contract.BackupPlanParams `json:"plans"`
}

// loadPlans reads the plans strictly. A missing file is none.
func loadPlans(p paths.Paths) ([]contract.BackupPlanParams, error) {
	var f plansFile
	if err := loadJSON(p.BackupPlansState(), &f); err != nil {
		return nil, err
	}
	return f.Plans, nil
}

func savePlans(p paths.Paths, plans []contract.BackupPlanParams) error {
	return saveJSON(p, p.BackupPlansState(), plansFile{Plans: plans})
}

// lockPlans keeps writers of plans.json apart: backup.plan runs under the
// server subject, site.destroy and db.drop under a site's.
func lockPlans(ctx context.Context, p paths.Paths) (func(), error) {
	if err := os.MkdirAll(p.LocksDir(), 0o700); err != nil {
		return nil, err
	}
	return sysexec.Lock(ctx, p.BackupPlansLock())
}

func findPlan(plans []contract.BackupPlanParams, id contract.PlanID) int {
	return slices.IndexFunc(plans, func(x contract.BackupPlanParams) bool { return x.ID == id })
}

func planNotFound(id contract.PlanID) error {
	return &contract.AgentError{Code: contract.ErrNotFound, Message: fmt.Sprintf("backup plan %s doesn't exist", id)}
}

// planBusy holds a plan's run lock, or answers busy while a run has it.
func planBusy(p paths.Paths, id contract.PlanID) (func(), error) {
	if err := os.MkdirAll(p.LocksDir(), 0o700); err != nil {
		return nil, err
	}
	unlock, ok, err := sysexec.TryLock(p.BackupLock(id))
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, &contract.AgentError{Code: contract.ErrBusy, Message: fmt.Sprintf("backup plan %s is running", id)}
	}
	return unlock, nil
}

// checkPlan answers not_found for a destination, site or database the plan
// names that doesn't exist. A database must be listed by a site.
func checkPlan(p paths.Paths, plan *contract.BackupPlanParams) error {
	ds, err := loadDestinations(p)
	if err != nil {
		return err
	}
	if find(ds, plan.Destination) < 0 {
		return notFound(plan.Destination)
	}
	all, err := sites.All(p)
	if err != nil {
		return err
	}
	for _, name := range plan.Sites {
		if !slices.ContainsFunc(all, func(s sites.State) bool { return s.Name == name }) {
			return &contract.AgentError{Code: contract.ErrNotFound, Message: fmt.Sprintf("site %s doesn't exist", name)}
		}
	}
	for _, db := range plan.Databases {
		if !slices.ContainsFunc(all, func(s sites.State) bool { return s.HasDatabase(db) }) {
			return &contract.AgentError{Code: contract.ErrNotFound, Message: fmt.Sprintf("no site has the database %s", db)}
		}
	}
	return nil
}

// SetPlan adds a plan or replaces one's settings, and installs its timer
// (R05-D7). A changed plan's timer restarts on its new schedule.
func SetPlan(ctx context.Context, d sysexec.Deps, plan *contract.BackupPlanParams) (contract.BackupPlanResult, error) {
	res := contract.BackupPlanResult{ID: plan.ID}
	unlock, err := lockPlans(ctx, d.Paths)
	if err != nil {
		return res, err
	}
	defer unlock()
	if err := checkPlan(d.Paths, plan); err != nil {
		return res, err
	}
	plans, err := loadPlans(d.Paths)
	if err != nil {
		return res, err
	}
	units, err := systemd.BackupUnits(plan.ID, plan.Schedule)
	if err != nil {
		return res, err
	}
	if err := systemd.InstallUnits(ctx, d, units); err != nil {
		return res, err
	}
	if i := findPlan(plans, plan.ID); i >= 0 {
		plans[i] = *plan
		if err := systemd.Systemctl(ctx, d, "restart", systemd.BackupTimer(plan.ID)); err != nil {
			return res, err
		}
	} else {
		plans = append(plans, *plan)
	}
	if err := savePlans(d.Paths, plans); err != nil {
		return res, err
	}
	d.Logf("Backup plan %s runs %s to %s", plan.ID, plan.Schedule, plan.Destination)
	return res, nil
}

// DeletePlan removes a plan and its units. Its backups stay on the
// destination, and its records until maintenance prunes them. A running
// plan answers busy.
func DeletePlan(ctx context.Context, d sysexec.Deps, id contract.PlanID) (contract.BackupPlanResult, error) {
	res := contract.BackupPlanResult{ID: id}
	unlock, err := lockPlans(ctx, d.Paths)
	if err != nil {
		return res, err
	}
	defer unlock()
	plans, err := loadPlans(d.Paths)
	if err != nil {
		return res, err
	}
	i := findPlan(plans, id)
	if i < 0 {
		return res, planNotFound(id)
	}
	release, err := planBusy(d.Paths, id)
	if err != nil {
		return res, err
	}
	defer release()
	if err := systemd.RemoveBackupUnits(ctx, d, id); err != nil {
		return res, err
	}
	dest := plans[i].Destination
	if err := savePlans(d.Paths, slices.Delete(plans, i, i+1)); err != nil {
		return res, err
	}
	d.Logf("Backup plan %s is removed; its backups stay on %s", id, dest)
	return res, nil
}

// ForgetSite removes a destroyed site from every plan (R05-D13). The plans
// and the site's backups stay; plans hold no unit text about sites, so
// nothing is rendered again.
func ForgetSite(p paths.Paths, site contract.SiteName) error {
	return editPlans(p, func(pl *contract.BackupPlanParams) {
		pl.Sites = slices.DeleteFunc(pl.Sites, func(s contract.SiteName) bool { return s == site })
	})
}

// ForgetDatabase removes a dropped database from every plan, which would
// otherwise fail on it at every run.
func ForgetDatabase(p paths.Paths, db contract.DatabaseName) error {
	return editPlans(p, func(pl *contract.BackupPlanParams) {
		pl.Databases = slices.DeleteFunc(pl.Databases, func(x contract.DatabaseName) bool { return x == db })
	})
}

// planLockWait bounds how long a site's job waits for plans.json.
const planLockWait = time.Minute

// editPlans applies edit to every plan and saves them if any changed.
func editPlans(p paths.Paths, edit func(*contract.BackupPlanParams)) error {
	if _, err := os.Stat(p.BackupPlansState()); errors.Is(err, fs.ErrNotExist) {
		return nil // no plans: nothing to lock or write
	}
	ctx, cancel := context.WithTimeout(context.Background(), planLockWait)
	defer cancel()
	unlock, err := lockPlans(ctx, p)
	if err != nil {
		return err
	}
	defer unlock()
	plans, err := loadPlans(p)
	if err != nil {
		return err
	}
	changed := false
	for i := range plans {
		before := len(plans[i].Sites) + len(plans[i].Databases)
		edit(&plans[i])
		changed = changed || len(plans[i].Sites)+len(plans[i].Databases) != before
	}
	if !changed {
		return nil
	}
	return savePlans(p, plans)
}
