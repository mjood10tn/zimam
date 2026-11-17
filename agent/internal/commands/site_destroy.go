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
	"zimam/agent/internal/valkey"
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
	// A site with a Valkey user needs Valkey installed to remove it. A site
	// without one never touches Valkey, so a server without Valkey destroys
	// sites as before.
	if st.CacheUserHash != "" {
		if err := requireValkey(d); err != nil {
			return sites.State{}, noMySQL, err
		}
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
// A site with a Valkey user needs Valkey installed and answering before
// anything changes, or the destroy is refused with a conflict. Its keys and
// user go first of all (valkey.RemoveSiteUser: purge, remove the user, purge
// again), and the hash leaves the state only after the last purge, so a
// failure leaves a site that is destroyed again by running the same steps. A
// site without a hash never touches Valkey.
//
// MySQL goes next, before Nginx, the pools and the site's user and files: a
// failure there leaves the site as it was, serving and able to be destroyed
// again. A database that is already dropped stays dropped (and out of the
// state file), and the rest are dropped when the command runs again. Once
// the site's own files are gone nothing could tell what MySQL still held.
// MySQL steps: for each database, revoke and drop it (as db.drop does),
// then drop the user's two accounts, which removes every privilege row they
// hold, so a site made later with the same name starts with none. The
// accounts are dropped whenever MySQL is installed, even for a site whose
// state lists neither databases nor a user; then a failure is only logged.
func destroySite(ctx context.Context, j *jobs.Context, p contract.Params) (any, error) {
	params := *p.(*contract.SiteDestroyParams)
	d := j.Deps()
	st, work, err := checkSiteDestroy(d, params)
	if err != nil {
		return nil, err
	}
	if st.CacheUserHash != "" {
		// Before anything changes: a Valkey that is down must not be found out
		// after databases were dropped.
		if err := requireValkeyRunning(ctx, d); err != nil {
			return nil, err
		}
		if work == requiredMySQL {
			// The Valkey steps can't be undone; MySQL, which comes after them, must
			// be known to answer first.
			if err := mysql.Ping(ctx, d); err != nil {
				return nil, dbError(err)
			}
		}
		if err := valkey.RemoveSiteUser(ctx, d, cacheUsers(d.Paths), params.Name, cachePurgeState(d, params.Name)); err != nil {
			return nil, err
		}
		d.Logf("Removed the Valkey user of site %s and its keys", params.Name)
		// The state on disk no longer has the hash or the purge progress; the copy in hand must not
		// write it back when a MySQL step saves it.
		st.CacheUserHash = ""
		st.CachePurge = nil
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

// siteDestroy runs the destroy. A failure keeps what the destroy knows: the
// job fails, and its result says which of the site's service users are no
// longer on the server (jobs.Landed), so the panel keeps the rows of the
// users that remain.
func siteDestroy(ctx context.Context, j *jobs.Context, p contract.Params) (any, error) {
	res, err := destroySite(ctx, j, p)
	if err == nil {
		return res, nil
	}
	// A stop or a timeout stays what it is: the job ends cancelled or timed
	// out, and the panel, with no result, forgets the credentials as it does
	// for any run that can't say how far it got.
	if ctx.Err() != nil {
		return nil, err
	}
	name := p.(*contract.SiteDestroyParams).Name
	removed, ok := removedUsers(j.Deps(), name)
	if !ok {
		return nil, err
	}
	return contract.SiteDestroyResult{Name: name, Removed: &removed}, &jobs.Landed{Err: err}
}

// removedUsers reads the state the destroy left on disk: a user the state
// doesn't list is gone, or was never made. A site with no state file has
// none. Its second result is false when the state can't be read, and
// nothing can be said.
func removedUsers(d sysexec.Deps, name contract.SiteName) (contract.SiteDestroyRemoved, bool) {
	st, err := sites.Load(d.Paths, name)
	if errors.Is(err, fs.ErrNotExist) {
		return contract.SiteDestroyRemoved{MySQLUser: true, ValkeyUser: true}, true
	}
	if err != nil {
		return contract.SiteDestroyRemoved{}, false
	}
	return contract.SiteDestroyRemoved{MySQLUser: !st.DBUser, ValkeyUser: st.CacheUserHash == ""}, true
}
