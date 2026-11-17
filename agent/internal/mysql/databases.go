package mysql

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"zimam/agent/contract"
	"zimam/agent/internal/services"
	"zimam/agent/internal/sysexec"
)

// Installed reports whether Zimam installed MySQL on this server.
func Installed(d sysexec.Deps) (bool, error) {
	st, err := services.Load(d.Paths)
	if err != nil {
		return false, err
	}
	return st.MySQL.Installed, nil
}

// CreateDatabase creates a database with the character set and collation
// every Zimam database has. It has no IF NOT EXISTS: a name that already
// exists is an error (ErrDatabaseExists), never adopted.
func CreateDatabase(name contract.DatabaseName) Statement {
	const kind = "create database"
	if err := name.Validate(); err != nil {
		return invalid(kind, "the database name", err)
	}
	return Statement{kind: kind, sql: "CREATE DATABASE " + quote(string(name)) + " CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci;"}
}

// DropDatabase drops a database; one that is already gone is no error.
func DropDatabase(name contract.DatabaseName) Statement {
	const kind = "drop database"
	if err := name.Validate(); err != nil {
		return invalid(kind, "the database name", err)
	}
	return Statement{kind: kind, sql: "DROP DATABASE IF EXISTS " + quote(string(name)) + ";"}
}

// GrantAll gives both of the site's accounts every privilege on the
// database, and nothing global.
func GrantAll(site contract.SiteName, name contract.DatabaseName) Statement {
	const kind = "grant on a database"
	if st, ok := checkSiteAndDatabase(kind, site, name); !ok {
		return st
	}
	return Statement{kind: kind, sql: "GRANT ALL PRIVILEGES ON " + grantTarget(name) + " TO " + accounts(site) + ";"}
}

// RevokeAll takes the privileges on the database from both of the site's
// accounts. It succeeds whether or not the accounts or the grant exist: a
// grant outlives its database, so a drop revokes first.
func RevokeAll(site contract.SiteName, name contract.DatabaseName) Statement {
	const kind = "revoke on a database"
	if st, ok := checkSiteAndDatabase(kind, site, name); !ok {
		return st
	}
	return Statement{kind: kind, sql: "REVOKE IF EXISTS ALL PRIVILEGES ON " + grantTarget(name) + " FROM " + accounts(site) + " IGNORE UNKNOWN USER;"}
}

// RevokeAllUnescaped is RevokeAll for a grant that was written with the name
// as it is, underscores unescaped. Such a grant is a pattern that also covers
// other names, and RevokeAll's escaped name does not match its row. Only a
// grant someone else made can be like that.
func RevokeAllUnescaped(site contract.SiteName, name contract.DatabaseName) Statement {
	const kind = "revoke on a database name as written"
	if st, ok := checkSiteAndDatabase(kind, site, name); !ok {
		return st
	}
	return Statement{kind: kind, sql: "REVOKE IF EXISTS ALL PRIVILEGES ON " + quote(string(name)) + ".* FROM " + accounts(site) + " IGNORE UNKNOWN USER;"}
}

// DatabaseGrantees selects the accounts that hold privileges on the
// database itself (not on a table, and not global): the user, the host and
// the database pattern of every row of mysql.db that covers the name. MySQL
// does the matching, so a grant written with escaped underscores, one with
// the name as it is and one with a wildcard such as s\_shop\_% all show up.
// The session sets its own sql_mode first: with NO_BACKSLASH_ESCAPES on the
// server, the backslashes in a stored pattern would stop matching.
func DatabaseGrantees(name contract.DatabaseName) Statement {
	const kind = "list grants on a database"
	if err := name.Validate(); err != nil {
		return invalid(kind, "the database name", err)
	}
	return Statement{kind: kind, sql: "SET SESSION sql_mode = '';\nSELECT User, Host, Db FROM mysql.db WHERE '" + string(name) + "' LIKE Db ORDER BY User, Host, Db;"}
}

// Grantee is one row of mysql.db that covers a database: an account, and
// the pattern the grant was written with.
type Grantee struct {
	User, Host, Pattern string
}

// Grantees returns the accounts that hold privileges on the database.
func Grantees(ctx context.Context, d sysexec.Deps, name contract.DatabaseName) ([]Grantee, error) {
	rows, err := Exec(ctx, d, DatabaseGrantees(name))
	if err != nil {
		return nil, err
	}
	var out []Grantee
	for _, r := range rows {
		if len(r) != 3 {
			return nil, fmt.Errorf("MySQL answered %q to the query for a database's grants", strings.Join(r, "\t"))
		}
		out = append(out, Grantee{User: r[0], Host: r[1], Pattern: r[2]})
	}
	return out, nil
}

// DatabaseSizes selects every schema's name and its size in bytes (data and
// indexes), an empty schema as 0. The statistics are cached for a day unless
// the session turns the cache off, so it does.
func DatabaseSizes() Statement {
	return Statement{kind: "database sizes", sql: "SET SESSION information_schema_stats_expiry = 0;\n" +
		"SELECT s.schema_name, COALESCE(SUM(t.data_length + t.index_length), 0) FROM information_schema.schemata s " +
		"LEFT JOIN information_schema.tables t ON t.table_schema = s.schema_name GROUP BY s.schema_name ORDER BY s.schema_name;"}
}

// DatabaseObjects selects how many tables (views included), routines and
// events a database holds, as one number. A name that passed Validate has
// no quote, so it can sit in a single-quoted literal.
func DatabaseObjects(name contract.DatabaseName) Statement {
	const kind = "count database objects"
	if err := name.Validate(); err != nil {
		return invalid(kind, "the database name", err)
	}
	n := "'" + string(name) + "'"
	return Statement{kind: kind, sql: "SELECT (SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = " + n +
		") + (SELECT COUNT(*) FROM information_schema.routines WHERE routine_schema = " + n +
		") + (SELECT COUNT(*) FROM information_schema.events WHERE event_schema = " + n + ");"}
}

// ObjectCount is how many tables, views, routines and events the database
// holds; 0 for an empty database and for one that doesn't exist.
func ObjectCount(ctx context.Context, d sysexec.Deps, name contract.DatabaseName) (int64, error) {
	rows, err := Exec(ctx, d, DatabaseObjects(name))
	if err != nil {
		return 0, err
	}
	if len(rows) != 1 || len(rows[0]) != 1 {
		return 0, fmt.Errorf("MySQL answered %v to the query for a database's objects", rows)
	}
	n, err := strconv.ParseInt(rows[0][0], 10, 64)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("MySQL answered %q to the query for a database's objects", rows[0][0])
	}
	return n, nil
}

// Sizes returns every schema's size in bytes by name, including MySQL's own.
func Sizes(ctx context.Context, d sysexec.Deps) (map[string]int64, error) {
	rows, err := Exec(ctx, d, DatabaseSizes())
	if err != nil {
		return nil, err
	}
	sizes := make(map[string]int64, len(rows))
	for _, r := range rows {
		var n int64
		if len(r) == 2 {
			n, err = strconv.ParseInt(r[1], 10, 64)
		}
		if len(r) != 2 || err != nil || n < 0 {
			return nil, fmt.Errorf("MySQL answered %q to the query for database sizes", strings.Join(r, "\t"))
		}
		sizes[r[0]] = n
	}
	return sizes, nil
}

// maxGrantNameLength is MySQL's limit on a database name in a GRANT or
// REVOKE, where every underscore is written with a backslash: a longer one
// fails with error 1059.
const maxGrantNameLength = 64

func checkSiteAndDatabase(kind string, site contract.SiteName, name contract.DatabaseName) (Statement, bool) {
	if err := site.Validate(); err != nil {
		return invalid(kind, "the site name", err), false
	}
	if err := name.Validate(); err != nil {
		return invalid(kind, "the database name", err), false
	}
	if n := len(name) + strings.Count(string(name), "_"); n > maxGrantNameLength {
		return invalid(kind, "the database name", fmt.Errorf("is %d characters with its underscores escaped, over MySQL's limit of %d for a grant", n, maxGrantNameLength)), false
	}
	return Statement{}, true
}

// quote puts a name in backquotes. Names come from types whose patterns
// have no backtick.
func quote(name string) string { return "`" + name + "`" }

// grantTarget is a GRANT or REVOKE's database part. In a grant the database
// name is a pattern, where _ matches any character, so every _ is escaped:
// an unescaped s_a_x would also give the user s_axx.
func grantTarget(name contract.DatabaseName) string {
	return quote(strings.ReplaceAll(string(name), "_", `\_`)) + ".*"
}
