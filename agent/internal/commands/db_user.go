package commands

import (
	"context"
	"fmt"
	"strings"

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
// The steps, and what a failure or a cancellation at each leaves. The last
// step is the one that commits: until it runs, a new account can't be logged
// into and an existing one keeps its password.
//
//  1. CREATE USER IF NOT EXISTS, locked and without a password. A new account
//     is unusable and holds no privilege. An account that exists is left as
//     it is: unlocked, with its old password and its grants. A failure leaves
//     nothing.
//  2. The state file records that the site has a user, so db.create grants
//     new databases to it and site.destroy drops it. It is saved before any
//     grant, so an account that holds privileges is always one the state
//     knows. If the save fails the account has no privilege yet.
//  3. GRANT ALL PRIVILEGES on each database, one statement each. Granting
//     again is harmless. A failure here leaves the grants made so far on an
//     account that is still locked (new) or still has its old password
//     (rotation), so the site keeps working with the credentials it has.
//  4. The accounts are checked: neither may hold a global privilege, a
//     role, or a privilege on any database that is not one of the site's
//     own. One that does is refused as a conflict, and it stays locked (new)
//     or keeps its old password (rotation), since MySQL users made or
//     changed by hand are not Zimam's to hand to a site.
//  5. ALTER USER ... IDENTIFIED BY ... ACCOUNT UNLOCK, for both accounts in
//     one statement, which MySQL applies to both or to neither. This is the
//     commit: the new password works and the old one stops, only after every
//     grant is in place. If it fails, nothing of it has happened.
//
// A failed or cancelled run is repeated by running the command again, which
// converges. The password travels in the statement on the client's stdin; the
// errors name the statement by its kind and the MySQL error number, never its
// text.
func dbUser(ctx context.Context, j *jobs.Context, p contract.Params) (any, error) {
	params := *p.(*contract.DbUserParams)
	d := j.Deps()
	st, err := checkDbUser(d, params)
	if err != nil {
		return nil, err
	}
	if _, err := mysql.Exec(ctx, d, mysql.CreateUser(params.Site)); err != nil {
		return nil, dbError(err)
	}
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
	if err := requireOnlyOwnPrivileges(ctx, d, params.Site, st.Databases); err != nil {
		return nil, err
	}
	if _, err := mysql.Exec(ctx, d, mysql.AlterUserPassword(params.Site, params.Password)); err != nil {
		return nil, dbError(err)
	}
	d.Logf("Set the password of site %s's MySQL user and unlocked it", params.Site)
	return contract.ServiceUserResult{Site: params.Site}, nil
}

// requireOnlyOwnPrivileges refuses to hand out an account that holds
// anything beyond grants on the site's own databases: global privileges,
// roles, proxy rights, the grant option, or a database, table, routine or
// column grant for a database the site does not list. A database grant
// counts as the site's own only with its name escaped, as Zimam writes it.
func requireOnlyOwnPrivileges(ctx context.Context, d sysexec.Deps, site contract.SiteName, own []contract.DatabaseName) error {
	held, err := mysql.AccountPrivileges(ctx, d, site)
	if err != nil {
		return dbError(err)
	}
	ownPlain := make(map[string]bool, len(own))
	ownEscaped := make(map[string]bool, len(own))
	for _, db := range own {
		ownPlain[string(db)] = true
		ownEscaped[strings.ReplaceAll(string(db), "_", `\_`)] = true
	}
	for _, p := range held {
		switch p.Kind {
		case mysql.PrivDatabase:
			// A database grant is a pattern: only the escaped name is exactly
			// this database, the name as it is also covers others.
			if ownEscaped[p.Object] {
				continue
			}
			// The site's own grant, in Zimam's form, on a database its state
			// doesn't list: a create that didn't finish.
			if name := unescapedGrant(p.Object); name != "" && name.BelongsTo(site) {
				return conflict("The MySQL account '%s'@'%s' holds a grant on the database %s, which site %s doesn't list: an earlier db.create didn't finish. Run db.create for %s to finish it.", p.User, p.Host, name, site, name)
			}
		case mysql.PrivTable, mysql.PrivRoutine, mysql.PrivColumn:
			if ownEscaped[p.Object] || ownPlain[p.Object] {
				continue
			}
		}
		what := p.Kind + " privilege"
		if p.Object != "" {
			what += " on " + p.Object
		}
		return conflict("The MySQL account '%s'@'%s' holds a %s that Zimam did not give. Zimam won't give it a password until that is removed.", p.User, p.Host, what)
	}
	return nil
}

// unescapedGrant is the database a grant pattern names when the pattern is
// Zimam's own form of a valid database name (every underscore escaped), and
// empty for anything else.
func unescapedGrant(pattern string) contract.DatabaseName {
	name := contract.DatabaseName(strings.ReplaceAll(pattern, `\_`, "_"))
	if name.Validate() != nil || strings.ReplaceAll(string(name), "_", `\_`) != pattern {
		return ""
	}
	return name
}
