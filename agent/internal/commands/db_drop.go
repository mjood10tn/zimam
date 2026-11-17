package commands

import (
	"context"
	"fmt"

	"zimam/agent/contract"
	"zimam/agent/internal/jobs"
	"zimam/agent/internal/mysql"
	"zimam/agent/internal/sites"
	"zimam/agent/internal/sysexec"
)

func init() {
	jobPrechecks["db.drop"] = func(d sysexec.Deps) jobs.Precheck {
		return func(_ context.Context, p contract.Params) error {
			_, err := checkDbDrop(d, *p.(*contract.DbDropParams))
			return err
		}
	}
	jobHandlers["db.drop"] = dbDrop
}

// checkDbDrop is db.drop's precheck, run again by the job. Only a database
// the site's state lists can be dropped: one that exists in MySQL but
// belongs to no site, or to another site, is not_found. It returns the
// site's state.
func checkDbDrop(d sysexec.Deps, p contract.DbDropParams) (sites.State, error) {
	st, err := loadDBSite(d, p.Site)
	if err != nil {
		return sites.State{}, err
	}
	if !st.HasDatabase(p.Database) {
		return sites.State{}, &contract.AgentError{Code: contract.ErrNotFound, Message: fmt.Sprintf("Site %s has no database %s.", p.Site, p.Database)}
	}
	if err := requireMySQL(d); err != nil {
		return sites.State{}, err
	}
	return st, nil
}

// dbDrop drops a site's database. It first revokes the grant from the
// site's accounts, because a grant outlives its database and would let the
// user create the database again; the revoke works whether or not the site
// has a user. Then it drops the database and removes it from the state
// file. A database that is already gone from MySQL is removed from the
// state file all the same, so running the command again converges.
func dbDrop(ctx context.Context, j *jobs.Context, p contract.Params) (any, error) {
	params := *p.(*contract.DbDropParams)
	d := j.Deps()
	st, err := checkDbDrop(d, params)
	if err != nil {
		return nil, err
	}
	db := params.Database
	if _, err := mysql.Exec(ctx, d, mysql.RevokeAll(params.Site, db), mysql.DropDatabase(db)); err != nil {
		return nil, dbError(err)
	}
	d.Logf("Dropped database %s", db)
	st.Databases = withoutDatabase(st.Databases, db)
	if err := sites.Save(d.Paths, st); err != nil {
		return nil, fmt.Errorf("the database %s is dropped, but site %s's state could not be saved; run the command again: %w", db, params.Site, err)
	}
	return contract.DatabaseResult{Site: params.Site, Database: db}, nil
}
