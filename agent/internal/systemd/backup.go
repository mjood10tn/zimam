package systemd

import (
	"context"
	"errors"
	"io/fs"
	"os"

	"zimam/agent/contract"
	"zimam/agent/internal/configtx"
	"zimam/agent/internal/sysexec"
	"zimam/agent/internal/templates"
)

// BackupTimer and BackupService are a backup plan's units (R05-D7).
func BackupTimer(plan contract.PlanID) string   { return "zimam-backup-" + string(plan) + ".timer" }
func BackupService(plan contract.PlanID) string { return "zimam-backup-" + string(plan) + ".service" }

// BackupUnits renders a plan's service, which the timer starts, and its
// timer.
func BackupUnits(plan contract.PlanID, schedule contract.BackupSchedule) ([]Unit, error) {
	data := templates.BackupUnit{Plan: plan, Schedule: schedule}
	service, err := templates.Render("systemd/zimam-backup.service", data)
	if err != nil {
		return nil, err
	}
	timer, err := templates.Render("systemd/zimam-backup.timer", data)
	if err != nil {
		return nil, err
	}
	return []Unit{{Name: BackupService(plan), Content: service}, {Name: BackupTimer(plan), Content: timer, Enable: true}}, nil
}

// RemoveBackupUnits stops and disables a plan's timer, deletes its units and
// the timer's Persistent= stamp, and reloads systemd. A run in progress is
// left to finish. A plan without units makes no systemctl call.
func RemoveBackupUnits(ctx context.Context, d sysexec.Deps, plan contract.PlanID) error {
	timer := d.Paths.SystemdUnit(BackupTimer(plan))
	if _, err := os.Lstat(timer); err == nil {
		if err := Systemctl(ctx, d, "disable", "--now", BackupTimer(plan)); err != nil {
			return err
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	changes := []configtx.Change{
		{Path: timer, Remove: true},
		{Path: d.Paths.SystemdUnit(BackupService(plan)), Remove: true},
		{Path: d.Paths.TimerStamp(BackupTimer(plan)), Remove: true},
	}
	if err := configtx.Apply(ctx, changes, func(context.Context) error { return nil }); err != nil {
		return err
	}
	return Systemctl(ctx, d, "daemon-reload")
}
