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
	jobPrechecks["db.user"] = func(d sysexec.Deps) jobs.Precheck {
		return func(_ context.Context, p contract.Params) error {
			_, err := checkDbUser(d, *p.(*contract.DbUserParams))
			return err
		}
	}
	jobHandlers["db.user"] = dbUser
}

// checkDbUser is db.user's precheck, run again by the job: the site must
// exist and MySQL must be installed. It returns the site's state.
func checkDbUser(d sysexec.Deps, p contract.DbUserParams) (sites.State, error) {
	st, err := loadDBSite(d, p.Site)
	if err != nil {
		return sites.State{}, err
	}
	if err := requireMySQL(d); err != nil {
		return sites.State{}, err
	}
	return st, nil
}

// dbUser creates the site's MySQL user, or sets a new password for the one it
// has, and gives it every privilege on each of the site's databases and
// nothing else. A site's user is two accounts with one password (localhost
// and 127.0.0.1); every statement covers both.
//
// The steps, and what a failure at each leaves:
//
//  1. CREATE USER IF NOT EXISTS, with the new password. An account that
//     exists keeps its password. If this fails nothing has changed. A new
//     account holds no privilege, so a password nobody learned opens nothing.
//  2. ALTER USER ... IDENTIFIED BY, always, so the account has the new
//     password whether it existed or not. A failure leaves the account as the
//     first step left it: new and without privileges, or with its old
//     password and its old grants.
//  3. The state file records that the site has a user, so db.create grants
//     new databases to it and site.destroy drops it. It is saved before any
//     grant, so an account that holds privileges is always one the state
//     knows. If the save fails the account has no privilege yet.
//  4. GRANT ALL PRIVILEGES on each database, one statement each. Granting
//     again is harmless, so rotating or repeating the command converges.
//
// A failed run is repeated by running the command again. The one failure
// that costs a working site something is a grant failing on a rotation, after
// the password changed: the site keeps its grants but its old password no
// longer works, and running the command again gives the panel a password it
// knows. The password travels in the statement on the client's stdin; the
// errors name the statement by its kind and the MySQL error number, never its
// text.
func dbUser(ctx context.Context, j *jobs.Context, p contract.Params) (any, error) {
	params := *p.(*contract.DbUserParams)
	d := j.Deps()
	st, err := checkDbUser(d, params)
	if err != nil {
		return nil, err
	}
	if _, err := mysql.Exec(ctx, d, mysql.CreateUser(params.Site, params.Password)); err != nil {
		return nil, dbError(err)
	}
	if _, err := mysql.Exec(ctx, d, mysql.AlterUserPassword(params.Site, params.Password)); err != nil {
		return nil, dbError(err)
	}
	d.Logf("Set the password of site %s's MySQL user", params.Site)
	if !st.DBUser {
		st.DBUser = true
		if err := sites.Save(d.Paths, st); err != nil {
			return nil, fmt.Errorf("the MySQL user of site %s exists, but its state could not be saved; run the command again: %w", params.Site, err)
		}
	}
	for _, db := range st.Databases {
		if _, err := mysql.Exec(ctx, d, mysql.GrantAll(params.Site, db)); err != nil {
			return nil, dbError(err)
		}
		d.Logf("Granted %s to the site's user", db)
	}
	return contract.ServiceUserResult{Site: params.Site}, nil
}
