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

// DatabaseSizes selects every schema's name and its size in bytes (data and
// indexes), an empty schema as 0. The statistics are cached for a day unless
// the session turns the cache off, so it does.
func DatabaseSizes() Statement {
	return Statement{kind: "database sizes", sql: "SET SESSION information_schema_stats_expiry = 0;\n" +
		"SELECT s.schema_name, COALESCE(SUM(t.data_length + t.index_length), 0) FROM information_schema.schemata s " +
		"LEFT JOIN information_schema.tables t ON t.table_schema = s.schema_name GROUP BY s.schema_name ORDER BY s.schema_name;"}
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

func checkSiteAndDatabase(kind string, site contract.SiteName, name contract.DatabaseName) (Statement, bool) {
	if err := site.Validate(); err != nil {
		return invalid(kind, "the site name", err), false
	}
	if err := name.Validate(); err != nil {
		return invalid(kind, "the database name", err), false
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
