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
	jobHandlers["backup.restore"] = backupRestore
}

// backupList answers backup.list: a plan's complete backups, from their
// manifests.
func backupList(d sysexec.Deps) Handler {
	return func(ctx context.Context, p contract.Params) (any, error) {
		return backup.List(ctx, d, p.(*contract.BackupPlanIDParams).Plan)
	}
}

// backupRestore restores a database or a site's files (R05-D10). A new
// database is made and, after a failed restore, dropped the way db.create
// and db.drop do it, with their checks.
func backupRestore(ctx context.Context, j *jobs.Context, p contract.Params) (any, error) {
	params := p.(*contract.BackupRestoreParams)
	d := j.Deps()
	env := backup.RestoreEnv{
		Helper: j.Helper,
		Lookup: helper.LookupAccount,
		CheckNew: func(db contract.DatabaseName) error {
			_, err := checkDbCreate(d, contract.DbCreateParams{Site: params.Site, Database: db})
			return err
		},
		Create: func(ctx context.Context, db contract.DatabaseName) error {
			_, err := dbCreate(ctx, j, &contract.DbCreateParams{Site: params.Site, Database: db})
			return err
		},
		Drop: func(ctx context.Context, db contract.DatabaseName) error {
			st, err := loadDBSite(d, params.Site)
			if err != nil {
				return err
			}
			_, err = dropDatabase(ctx, d, st, db)
			return err
		},
	}
	return backup.Restore(ctx, d, env, params)
}
