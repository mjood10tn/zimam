package commands

import (
	"context"
	"errors"
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
	if _, err := mysql.Exec(ctx, d, mysql.RevokeAll(params.Site, db)); err != nil {
		return nil, dbError(err)
	}
	if _, err := mysql.Exec(ctx, d, mysql.DropDatabase(db)); err != nil {
		err = dbError(err)
		var ae *contract.AgentError
		if errors.As(err, &ae) {
			return nil, &contract.AgentError{Code: ae.Code, Message: ae.Message + revokedNotDropped(db)}
		}
		return nil, fmt.Errorf("%w. %s", err, revokedNotDropped(db))
	}
	d.Logf("Dropped database %s", db)
	st.Databases = withoutDatabase(st.Databases, db)
	if err := sites.Save(d.Paths, st); err != nil {
		return nil, fmt.Errorf("the database %s is dropped, but site %s's state could not be saved; run the command again: %w", db, params.Site, err)
	}
	return contract.DatabaseResult{Site: params.Site, Database: db}, nil
}

// revokedNotDropped is what a failed DROP DATABASE adds to its error: the
// grant is already gone, so the site's user can't reach the database, and
// the state file still lists it.
func revokedNotDropped(db contract.DatabaseName) string {
	return fmt.Sprintf(" The site's user no longer has access to %s, and the database is still listed; run the command again to finish dropping it.", db)
}
