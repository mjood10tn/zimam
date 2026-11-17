package commands

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"zimam/agent/contract"
	"zimam/agent/internal/jobs"
	"zimam/agent/internal/mysql"
	"zimam/agent/internal/sites"
	"zimam/agent/internal/sysexec"
)

func init() {
	jobPrechecks["db.create"] = func(d sysexec.Deps) jobs.Precheck {
		return func(_ context.Context, p contract.Params) error {
			_, err := checkDbCreate(d, *p.(*contract.DbCreateParams))
			return err
		}
	}
	jobHandlers["db.create"] = dbCreate
}

// checkDbCreate is db.create's precheck, run again by the job: the site
// must exist, MySQL must be installed, and the name must be free. A name is
// taken when any site lists it. A name also belongs to one site only: when
// the prefixes of two sites fit it (s_shop_x_main starts with the prefixes
// of shop and of shop-x), it belongs to the site with the longest prefix,
// shop-x, and only that site may create it. It returns the site's state.
func checkDbCreate(d sysexec.Deps, p contract.DbCreateParams) (sites.State, error) {
	st, err := loadDBSite(d, p.Site)
	if err != nil {
		return sites.State{}, err
	}
	if err := requireMySQL(d); err != nil {
		return sites.State{}, err
	}
	all, err := sites.All(d.Paths)
	if err != nil {
		return sites.State{}, err
	}
	for _, s := range all {
		if !s.HasDatabase(p.Database) {
			continue
		}
		if s.Name == p.Site {
			return sites.State{}, conflict("Site %s already has the database %s.", p.Site, p.Database)
		}
		return sites.State{}, conflict("The database %s belongs to site %s.", p.Database, s.Name)
	}
	if owner, ok := p.Database.OwnerAmong(siteNames(all)); ok && owner != p.Site {
		return sites.State{}, conflict("The name %s starts with the prefix of site %s (%s), which is longer than site %s's, so it belongs to site %s. Choose another name.",
			p.Database, owner, contract.DatabasePrefix(owner), p.Site, owner)
	}
	return st, nil
}

// dbCreate creates a database for a site: CREATE DATABASE, then the grant
// to the site's user if it has one, then the state file.
//
// A name that already exists in MySQL, though no site lists it, is taken
// over when it is empty: it can be what an earlier create left behind when
// its job was cancelled or its cleanup failed. One that holds a table, view,
// routine or event is a conflict, and is never touched.
//
// If a step after the create fails, the database, which is still empty, is
// dropped again and its grant revoked, so MySQL and the state file agree. If
// that cleanup fails too, the error says so, and creating the name again
// takes the empty database over. The create itself is not undone when its
// outcome is unknown (the job was cancelled or timed out): the next create
// finds the database and takes it over.
func dbCreate(ctx context.Context, j *jobs.Context, p contract.Params) (any, error) {
	params := *p.(*contract.DbCreateParams)
	d := j.Deps()
	st, err := checkDbCreate(d, params)
	if err != nil {
		return nil, err
	}
	db := params.Database
	if _, err := mysql.Exec(ctx, d, mysql.CreateDatabase(db)); err != nil {
		var me *mysql.Error
		if !errors.As(err, &me) || me.Errno() != mysql.ErrDatabaseExists {
			return nil, dbError(err)
		}
		n, cerr := mysql.ObjectCount(ctx, d, db)
		if cerr != nil {
			return nil, dbError(cerr)
		}
		if n > 0 {
			return nil, conflict("A database named %s already exists in MySQL with content in it, and no site lists it, so Zimam won't use it.", db)
		}
		d.Logf("Database %s already exists in MySQL, empty, and no site lists it; taking it over", db)
	} else {
		d.Logf("Created database %s", db)
	}
	undo := func(cause error) error {
		uctx, cancel := undoCtx(ctx)
		defer cancel()
		_, uerr := mysql.Exec(uctx, d, mysql.RevokeAll(params.Site, db), mysql.DropDatabase(db))
		if uerr == nil {
			d.Logf("Dropped the new database %s again", db)
			return cause
		}
		d.Logf("Could not drop the new database %s again: %v", db, uerr)
		extra := fmt.Sprintf("The empty database %s was left in MySQL (cleaning up failed: %v); creating it again will take it over.", db, uerr)
		// The job records only an AgentError's own message, so the text goes
		// into a copy of it.
		var ae *contract.AgentError
		if errors.As(cause, &ae) {
			return &contract.AgentError{Code: ae.Code, Message: ae.Message + " " + extra}
		}
		return fmt.Errorf("%w. %s", cause, extra)
	}
	if st.DBUser {
		if _, err := mysql.Exec(ctx, d, mysql.GrantAll(params.Site, db)); err != nil {
			return nil, undo(dbError(err))
		}
		d.Logf("Granted %s to the site's user", db)
	}
	st.Databases = append(slices.Clone(st.Databases), db)
	slices.Sort(st.Databases)
	if err := sites.Save(d.Paths, st); err != nil {
		return nil, undo(fmt.Errorf("saving site %s's state: %w", params.Site, err))
	}
	return contract.DatabaseResult{Site: params.Site, Database: db}, nil
}
