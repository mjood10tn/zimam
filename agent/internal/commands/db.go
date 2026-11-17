package commands

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"strings"
	"time"

	"zimam/agent/contract"
	"zimam/agent/internal/mysql"
	"zimam/agent/internal/sites"
	"zimam/agent/internal/sysexec"
)

// The database commands keep a site's databases in its state file and in
// MySQL. A database belongs to the site whose state lists it: the commands
// never touch a database that no site lists, even when its name carries a
// site's prefix.

func conflict(format string, args ...any) error {
	return &contract.AgentError{Code: contract.ErrConflict, Message: fmt.Sprintf(format, args...)}
}

// loadDBSite is the state of the site a database command acts for.
func loadDBSite(d sysexec.Deps, name contract.SiteName) (sites.State, error) {
	st, err := sites.Load(d.Paths, name)
	if errors.Is(err, fs.ErrNotExist) {
		return sites.State{}, &contract.AgentError{Code: contract.ErrNotFound, Message: fmt.Sprintf("Site %s doesn't exist.", name)}
	}
	return st, err
}

// requireMySQL refuses a server where Zimam didn't install MySQL.
func requireMySQL(d sysexec.Deps) error {
	ok, err := mysql.Installed(d)
	if err != nil {
		return err
	}
	if !ok {
		return conflict("MySQL isn't installed on this server. Install it first.")
	}
	return nil
}

// dbError turns a failure of MySQL into what the panel can show: a server
// that doesn't answer is a conflict with the server's state, and anything
// else stays an internal error that names the statement and not its text.
func dbError(err error) error {
	if mysql.IsDown(err) {
		return conflict("MySQL isn't running. Start it and try again.")
	}
	return err
}

// undoTimeout bounds putting MySQL back after a failure, which runs even
// when the job's own context has ended.
const undoTimeout = 2 * time.Minute

// undoCtx is a context for a rollback that survives the job's cancellation.
func undoCtx(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), undoTimeout)
}

// siteNames lists the names of the sites in all.
func siteNames(all []sites.State) []contract.SiteName {
	names := make([]contract.SiteName, len(all))
	for i, s := range all {
		names[i] = s.Name
	}
	return names
}

func withoutDatabase(dbs []contract.DatabaseName, name contract.DatabaseName) []contract.DatabaseName {
	out := slices.DeleteFunc(slices.Clone(dbs), func(n contract.DatabaseName) bool { return n == name })
	if len(out) == 0 {
		return nil // an empty list leaves the field out of the file
	}
	return out
}

// siteAccount is the site a MySQL account belongs to, if it has the shape of
// one of Zimam's: the user s_<site name> on localhost or 127.0.0.1. The
// account's text comes from MySQL, so the site name is checked before it
// reaches a statement.
func siteAccount(g mysql.Grantee) (contract.SiteName, bool) {
	name, ok := strings.CutPrefix(g.User, "s_")
	if !ok || (g.Host != "localhost" && g.Host != "127.0.0.1") {
		return "", false
	}
	site := contract.SiteName(name)
	if site.Validate() != nil {
		return "", false
	}
	return site, true
}

// clearStaleGrants makes sure no account holds privileges on the database
// before the site's own grant is made. A grant outlives its database, and a
// database made by hand can carry grants, so a name that is free in the
// state can still be reachable by another account, which would break the
// isolation of sites. The accounts that hold a grant fall in two kinds:
//
//   - a site's own accounts, with the grant on exactly this name (escaped, as
//     Zimam writes it, or as it is): what an earlier create left behind when
//     its cleanup failed. These are revoked, and the site's grant is made anew
//     afterwards;
//   - anything else, another user or host, a wildcard pattern such as
//     s\_shop\_%, or a grant on a table, routine or column of the database
//     (Zimam makes none, so one was made by hand and is never removed on
//     its own): a conflict, and nothing is changed for it.
//
// The query is asked again after the revokes, and anything left refuses.
func clearStaleGrants(ctx context.Context, d sysexec.Deps, db contract.DatabaseName) error {
	grantees, err := mysql.Grantees(ctx, d, db)
	if err != nil {
		return dbError(err)
	}
	escaped := strings.ReplaceAll(string(db), "_", `\_`)
	type leftover struct {
		site    contract.SiteName
		escaped bool
	}
	var todo []leftover
	for _, g := range grantees {
		site, ok := siteAccount(g)
		if !ok || g.Kind != mysql.PrivDatabase || (g.Pattern != escaped && g.Pattern != string(db)) {
			return foreignGrant(db, g)
		}
		l := leftover{site, g.Pattern == escaped}
		if !slices.Contains(todo, l) {
			todo = append(todo, l)
		}
	}
	if len(todo) == 0 {
		return nil
	}
	for _, l := range todo {
		d.Logf("Revoking the grant on %s that site %s's user was left with", db, l.site)
		revoke := mysql.RevokeAllUnescaped(l.site, db)
		if l.escaped {
			revoke = mysql.RevokeAll(l.site, db)
		}
		if _, err := mysql.Exec(ctx, d, revoke); err != nil {
			return dbError(err)
		}
	}
	if grantees, err = mysql.Grantees(ctx, d, db); err != nil {
		return dbError(err)
	}
	if len(grantees) > 0 {
		return foreignGrant(db, grantees[0])
	}
	return nil
}

func foreignGrant(db contract.DatabaseName, g mysql.Grantee) error {
	what := "a grant on " + g.Pattern
	if g.Kind != mysql.PrivDatabase {
		what = "a " + g.Kind + " grant in " + g.Pattern
	}
	return conflict("The database %s exists in MySQL with privileges that Zimam did not give: the account '%s'@'%s' holds %s. Zimam won't use it until that grant is removed.",
		db, g.User, g.Host, what)
}
