package commands

import (
	"context"

	"zimam/agent/contract"
	"zimam/agent/internal/backup"
	"zimam/agent/internal/helper"
	"zimam/agent/internal/jobs"
	"zimam/agent/internal/sysexec"
)

func init() {
	jobHandlers["backup.destination"] = func(ctx context.Context, j *jobs.Context, p contract.Params) (any, error) {
		return backup.SetDestination(ctx, j.Deps(), p.(*contract.BackupDestinationParams))
	}
	jobHandlers["backup.destination.delete"] = func(ctx context.Context, j *jobs.Context, p contract.Params) (any, error) {
		return backup.DeleteDestination(ctx, j.Deps(), p.(*contract.BackupDestinationIDParams).ID)
	}
	jobHandlers["backup.destination.test"] = func(ctx context.Context, j *jobs.Context, p contract.Params) (any, error) {
		return backup.TestDestination(ctx, j.Deps(), p.(*contract.BackupDestinationIDParams).ID)
	}
	jobHandlers["backup.plan"] = func(ctx context.Context, j *jobs.Context, p contract.Params) (any, error) {
		return backup.SetPlan(ctx, j.Deps(), p.(*contract.BackupPlanParams))
	}
	jobHandlers["backup.plan.delete"] = func(ctx context.Context, j *jobs.Context, p contract.Params) (any, error) {
		return backup.DeletePlan(ctx, j.Deps(), p.(*contract.BackupPlanIDParams).Plan)
	}
	jobHandlers["backup.run"] = func(ctx context.Context, j *jobs.Context, p contract.Params) (any, error) {
		env := backup.RunEnv{Helper: j.Helper, Lookup: helper.LookupAccount, LogGID: j.LogGID}
		return backup.RunPlan(ctx, j.Deps(), env, p.(*contract.BackupRunParams).Plan, contract.TriggerPanel)
	}
}

// backupRuns answers backup.runs: the run records the panel polls (R05-D9).
func backupRuns(d sysexec.Deps) Handler {
	return func(_ context.Context, p contract.Params) (any, error) {
		params := p.(*contract.BackupRunsParams)
		return backup.Runs(d.Paths, params.Plan, params.Since)
	}
}

// backupDestinationKeys answers backup.destination.keys: a destination's
// crypt keys, read back from rclone.conf for "Show keys again" (OQ2). It
// returns secrets, so the dispatcher audits it with the keys redacted.
func backupDestinationKeys(d sysexec.Deps) Handler {
	return func(_ context.Context, p contract.Params) (any, error) {
		return backup.DestinationKeys(d.Paths, p.(*contract.BackupDestinationIDParams).ID)
	}
}
