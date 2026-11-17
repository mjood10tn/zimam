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
// must exist, MySQL must be installed, and the name must be free. A name
// is taken when any site lists it, and also when the prefix of another
// existing site covers it (s_shop_x_main starts with the prefixes of shop
// and of shop-x), since two sites' databases could then not be told apart.
// It returns the site's state.
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
	for _, other := range claimedBy(all, p.Database) {
		if other != p.Site {
			return sites.State{}, conflict("The name %s also starts with the prefix of site %s (%s), so it could belong to either site. Choose another name.",
				p.Database, other, contract.DatabasePrefix(other))
		}
	}
	return st, nil
}

// dbCreate creates a database for a site: CREATE DATABASE, then the grant
// to the site's user if it has one, then the state file. A name that
// already exists in MySQL but that no site lists is a conflict: it is never
// adopted, and MySQL's refusal is also what decides when jobs of different
// sites ask for one name at once. If a step after the create fails, the
// database, which is still empty, is dropped again and its grant revoked,
// so MySQL and the state file agree.
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
		if errors.As(err, &me) && me.Errno() == mysql.ErrDatabaseExists {
			return nil, conflict("A database named %s already exists in MySQL, and no site lists it, so Zimam won't use it.", db)
		}
		return nil, dbError(err)
	}
	d.Logf("Created database %s", db)
	undo := func(cause error) error {
		uctx, cancel := undoCtx(ctx)
		defer cancel()
		if _, uerr := mysql.Exec(uctx, d, mysql.RevokeAll(params.Site, db), mysql.DropDatabase(db)); uerr != nil {
			return fmt.Errorf("%w; and the new database %s could not be dropped again, drop it by hand: %v", cause, db, uerr)
		}
		d.Logf("Dropped the new database %s again", db)
		return cause
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
