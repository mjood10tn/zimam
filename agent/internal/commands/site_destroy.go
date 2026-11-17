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

// checkSiteDestroy is site.destroy's precheck, run again by the job, before
// anything changes. The site must have something on the server. A site whose
// state lists databases is refused unless the params ask to drop them. A site
// with databases to drop or a MySQL user needs MySQL installed. A site with
// neither never touches MySQL, so a server without it destroys sites as
// before. It returns the site's state and whether MySQL has work to do; a
// site whose provisioning failed has no state file and no work.
func checkSiteDestroy(d sysexec.Deps, p contract.SiteDestroyParams) (sites.State, bool, error) {
	if err := sites.CheckDestroy(d, helper.LookupAccount, p.Name); err != nil {
		return sites.State{}, false, err
	}
	st, err := sites.Load(d.Paths, p.Name)
	if errors.Is(err, fs.ErrNotExist) {
		return sites.State{}, false, nil
	}
	if err != nil {
		return sites.State{}, false, err
	}
	if len(st.Databases) > 0 && !p.DropDatabases {
		names := make([]string, len(st.Databases))
		for i, db := range st.Databases {
			names[i] = string(db)
		}
		return sites.State{}, false, conflict("Site %s still has the databases %s. Delete the site with its databases, or drop the databases first.", p.Name, strings.Join(names, ", "))
	}
	if len(st.Databases) == 0 && !st.DBUser {
		return st, false, nil
	}
	if err := requireMySQL(d); err != nil {
		return sites.State{}, false, err
	}
	return st, true, nil
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
// hold, so a site made later with the same name starts with none.
func siteDestroy(ctx context.Context, j *jobs.Context, p contract.Params) (any, error) {
	params := *p.(*contract.SiteDestroyParams)
	d := j.Deps()
	st, work, err := checkSiteDestroy(d, params)
	if err != nil {
		return nil, err
	}
	if work {
		for _, db := range slices.Clone(st.Databases) {
			if st, err = dropDatabase(ctx, d, st, db); err != nil {
				return nil, err
			}
		}
		// The accounts go even when the state has no user: a user whose state
		// could not be saved after its creation would otherwise stay.
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
	}
	return sites.Destroy(ctx, d, j.Helper, helper.LookupAccount, params.Name)
}
