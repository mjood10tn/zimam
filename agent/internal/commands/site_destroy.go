package commands

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"strings"

	"zimam/agent/contract"
	"zimam/agent/internal/helper"
	"zimam/agent/internal/jobs"
	"zimam/agent/internal/mysql"
	"zimam/agent/internal/sites"
	"zimam/agent/internal/sysexec"
)

func init() {
	jobPrechecks["site.destroy"] = func(d sysexec.Deps) jobs.Precheck {
		return func(_ context.Context, p contract.Params) error {
			_, _, err := checkSiteDestroy(d, *p.(*contract.SiteDestroyParams))
			return err
		}
	}
	jobHandlers["site.destroy"] = siteDestroy
}

// How much of site.destroy touches MySQL.
type mysqlWork int

const (
	// noMySQL: the site has nothing in MySQL and MySQL isn't installed, or
	// the site has no state. A server without MySQL destroys sites as before.
	noMySQL mysqlWork = iota
	// requiredMySQL: the state lists databases to drop or a user, so MySQL
	// must be installed and running, or the destroy is refused.
	requiredMySQL
	// tidyMySQL: the state lists nothing, but MySQL is installed, so the
	// site's accounts are dropped if MySQL answers. A user whose state could
	// not be saved after its creation is removed this way. A failure here never
	// stops the destroy: nothing says the site has anything there.
	tidyMySQL
)

// checkSiteDestroy is site.destroy's precheck, run again by the job, before
// anything changes. The site must have something on the server. A site whose
// state lists databases is refused unless the params ask to drop them. A site
// with databases to drop or a MySQL user needs MySQL installed and running
// (the destroy is refused while it is down or removed, because the site's
// databases and user could not be dropped). It returns the site's state and
// what the destroy does in MySQL; a site whose provisioning failed has no
// state file and no work.
func checkSiteDestroy(d sysexec.Deps, p contract.SiteDestroyParams) (sites.State, mysqlWork, error) {
	if err := sites.CheckDestroy(d, helper.LookupAccount, p.Name); err != nil {
		return sites.State{}, noMySQL, err
	}
	st, err := sites.Load(d.Paths, p.Name)
	if errors.Is(err, fs.ErrNotExist) {
		return sites.State{}, noMySQL, nil
	}
	if err != nil {
		return sites.State{}, noMySQL, err
	}
	if len(st.Databases) > 0 && !p.DropDatabases {
		names := make([]string, len(st.Databases))
		for i, db := range st.Databases {
			names[i] = string(db)
		}
		return sites.State{}, noMySQL, conflict("Site %s still has the databases %s. Delete the site with its databases, or drop the databases first.", p.Name, strings.Join(names, ", "))
	}
	if len(st.Databases) == 0 && !st.DBUser {
		if ok, err := mysql.Installed(d); err == nil && ok {
			return st, tidyMySQL, nil
		}
		return st, noMySQL, nil
	}
	if err := requireMySQL(d); err != nil {
		return sites.State{}, noMySQL, err
	}
	return st, requiredMySQL, nil
}

// siteDestroy removes the site (§9.8). A site already gone answers
// not_found, as the precheck does.
//
// MySQL goes first, before Nginx, the pools and the site's user and files: a
// failure there leaves the site as it was, serving and able to be destroyed
// again. A database that is already dropped stays dropped (and out of the
// state file), and the rest are dropped when the command runs again. Once
// the site's own files are gone nothing could tell what MySQL still held.
// MySQL steps: for each database, revoke and drop it (as db.drop does),
// then drop the user's two accounts, which removes every privilege row they
// hold, so a site made later with the same name starts with none. The
// accounts are dropped whenever MySQL is installed, even for a site whose
// state lists neither databases nor a user; then a failure is only logged.
func siteDestroy(ctx context.Context, j *jobs.Context, p contract.Params) (any, error) {
	params := *p.(*contract.SiteDestroyParams)
	d := j.Deps()
	st, work, err := checkSiteDestroy(d, params)
	if err != nil {
		return nil, err
	}
	switch work {
	case requiredMySQL:
		for _, db := range slices.Clone(st.Databases) {
			if st, err = dropDatabase(ctx, d, st, db); err != nil {
				return nil, err
			}
		}
		if _, err := mysql.Exec(ctx, d, mysql.DropUser(params.Name)); err != nil {
			return nil, dbError(err)
		}
		d.Logf("Dropped the MySQL user of site %s", params.Name)
		if st.DBUser {
			st.DBUser = false
			if err := sites.Save(d.Paths, st); err != nil {
				return nil, fmt.Errorf("the MySQL user of site %s is dropped, but its state could not be saved; run the command again: %w", params.Name, err)
			}
		}
	case tidyMySQL:
		if _, err := mysql.Exec(ctx, d, mysql.DropUser(params.Name)); err != nil {
			d.Logf("Could not check the MySQL user of site %s, which the site's state doesn't list: %v", params.Name, err)
		}
	}
	return sites.Destroy(ctx, d, j.Helper, helper.LookupAccount, params.Name)
}
