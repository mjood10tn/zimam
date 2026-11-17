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

// dbCreate creates a database for a site: CREATE DATABASE, then the grants
// that already cover the name are cleared, then the grant to the site's user
// if it has one, then the state file.
//
// A name that already exists in MySQL, though no site lists it, is taken
// over when it is empty: it can be what an earlier create left behind when
// its job was cancelled or its cleanup failed. One that holds a table, view,
// routine or event is a conflict, and is never touched.
//
// Whether the database is new or taken over, no account but the site's own
// may hold privileges on it when the job ends (clearStaleGrants).
//
// If a step after the create fails, the database, which is still empty, is
// dropped again and its grant revoked, so MySQL and the state file agree. A
// database that was taken over existed before: only the grant is revoked,
// and the database stays as it was. If that cleanup fails too, the database is
// listed in the state anyway, as the site's, and the error says so (and that
// db.drop removes it); if the state cannot be saved either, the error says the
// database was left and that creating the name again adopts it.
// The create itself is not undone when its outcome is unknown (the job was
// cancelled or timed out): the next create finds the database and takes it
// over.
func dbCreate(ctx context.Context, j *jobs.Context, p contract.Params) (any, error) {
	params := *p.(*contract.DbCreateParams)
	d := j.Deps()
	st, err := checkDbCreate(d, params)
	if err != nil {
		return nil, err
	}
	db := params.Database
	created := true
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
		created = false
		d.Logf("Database %s already exists in MySQL, empty, and no site lists it; taking it over", db)
	} else {
		d.Logf("Created database %s", db)
	}
	undo := func(cause error, cleared bool) error {
		uctx, cancel := undoCtx(ctx)
		defer cancel()
		statements := []mysql.Statement{mysql.RevokeAll(params.Site, db)}
		if created {
			statements = append(statements, mysql.DropDatabase(db))
		}
		_, uerr := mysql.Exec(uctx, d, statements...)
		if uerr == nil {
			if created {
				d.Logf("Dropped the new database %s again", db)
			} else {
				d.Logf("Revoked the grant on %s again; the database existed before and is left as it was", db)
			}
			return cause
		}
		if created {
			d.Logf("Could not drop the new database %s again: %v", db, uerr)
		} else {
			d.Logf("Could not revoke the grant on %s again: %v", db, uerr)
		}
		// Past the grant clearing, the database holds only the site's own
		// grant: listing it in the state makes it the site's (db.list shows
		// it, db.user accepts its grant, db.drop removes it). Before that, it
		// may still carry another account's grant, so it stays unlisted and a
		// new create clears that grant again.
		var extra string
		if !cleared {
			extra = fmt.Sprintf("The empty database %s was left in MySQL (cleaning up failed: %v); creating it again will take it over.", db, uerr)
		} else if serr := keepDatabase(d, st, db); serr == nil {
			d.Logf("Listed %s in site %s's state, since cleaning up failed", db, params.Site)
			extra = fmt.Sprintf("The database %s was kept (cleaning up failed: %v) and is listed under the site; drop it with db.drop if it isn't wanted.", db, uerr)
		} else {
			d.Logf("Could not list %s in site %s's state: %v", db, params.Site, serr)
			extra = fmt.Sprintf("The database %s was left in MySQL, not in the site's state (cleaning up failed: %v; saving the state failed: %v). Running db.create for %s again adopts it.", db, uerr, serr, db)
		}
		// The job records only an AgentError's own message, so the text goes
		// into a copy of it.
		var ae *contract.AgentError
		if errors.As(cause, &ae) {
			return &contract.AgentError{Code: ae.Code, Message: ae.Message + " " + extra}
		}
		return fmt.Errorf("%w. %s", cause, extra)
	}
	if err := clearStaleGrants(ctx, d, db); err != nil {
		if created {
			return nil, undo(err, false)
		}
		return nil, err
	}
	if st.DBUser {
		if _, err := mysql.Exec(ctx, d, mysql.GrantAll(params.Site, db)); err != nil {
			return nil, undo(dbError(err), true)
		}
		d.Logf("Granted %s to the site's user", db)
	}
	st.Databases = append(slices.Clone(st.Databases), db)
	slices.Sort(st.Databases)
	if err := sites.Save(d.Paths, st); err != nil {
		return nil, undo(fmt.Errorf("saving site %s's state: %w", params.Site, err), true)
	}
	return contract.DatabaseResult{Site: params.Site, Database: db}, nil
}

// keepDatabase saves the site's state with db added to its databases.
func keepDatabase(d sysexec.Deps, st sites.State, db contract.DatabaseName) error {
	if !slices.Contains(st.Databases, db) {
		st.Databases = append(slices.Clone(st.Databases), db)
		slices.Sort(st.Databases)
	}
	return sites.Save(d.Paths, st)
}
