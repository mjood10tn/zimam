package commands

import (
	"context"

	"zimam/agent/contract"
	"zimam/agent/internal/backup"
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
}

// backupDestinationKeys answers backup.destination.keys: a destination's
// crypt keys, read back from rclone.conf for "Show keys again" (OQ2). It
// returns secrets, so the dispatcher audits it with the keys redacted.
func backupDestinationKeys(d sysexec.Deps) Handler {
	return func(_ context.Context, p contract.Params) (any, error) {
		return backup.DestinationKeys(d.Paths, p.(*contract.BackupDestinationIDParams).ID)
	}
}
