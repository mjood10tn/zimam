package mysql

import (
	"context"
	"fmt"
	"strings"

	"zimam/agent/contract"
	"zimam/agent/internal/sysexec"
)

// A site has two accounts, with one password and the same grants:
// 's_<name>'@'localhost' for the socket and 's_<name>'@'127.0.0.1' for TCP.
// Under skip_name_resolve a TCP connection from 127.0.0.1 doesn't match a
// localhost account. Each statement covers both accounts in one SQL
// statement, which MySQL applies to both or to neither.

// accounts is the site's two accounts as a list. Site names have only
// lowercase letters, digits and hyphens, which are safe in a quoted name.
func accounts(site contract.SiteName) string {
	user := "'s_" + string(site) + "'"
	return user + "@'localhost', " + user + "@'127.0.0.1'"
}

// passwordAccounts is the accounts, each with IDENTIFIED BY and the
// password. The password's pattern has no quote or backslash.
func passwordAccounts(site contract.SiteName, pw contract.ServicePassword) string {
	user := "'s_" + string(site) + "'"
	by := " IDENTIFIED BY '" + string(pw) + "'"
	return user + "@'localhost'" + by + ", " + user + "@'127.0.0.1'" + by
}

func checkSiteAndPassword(kind string, site contract.SiteName, pw contract.ServicePassword) (Statement, bool) {
	if err := site.Validate(); err != nil {
		return invalid(kind, "the site name", err), false
	}
	if err := pw.Validate(); err != nil {
		return invalid(kind, "the password", err), false
	}
	return Statement{}, true
}

// CreateUser creates the site's two accounts if they don't exist, locked and
// without a password, so a new account can't be used until
// AlterUserPassword sets its password and unlocks it. An account that exists
// is left as it is: its password, its grants and whether it is locked.
func CreateUser(site contract.SiteName) Statement {
	const kind = "create user"
	if err := site.Validate(); err != nil {
		return invalid(kind, "the site name", err)
	}
	return Statement{kind: kind, sql: "CREATE USER IF NOT EXISTS " + accounts(site) + " ACCOUNT LOCK;"}
}

// AlterUserPassword sets the password of both of the site's accounts and
// unlocks them, in one statement: MySQL applies it to both or to neither, so
// this is the step that makes the user usable.
func AlterUserPassword(site contract.SiteName, pw contract.ServicePassword) Statement {
	const kind = "alter user"
	if st, ok := checkSiteAndPassword(kind, site, pw); !ok {
		return st
	}
	return Statement{kind: kind, secret: true, sql: "ALTER USER " + passwordAccounts(site, pw) + " ACCOUNT UNLOCK;"}
}

// DropUser drops both of the site's accounts, which takes their grants with
// them. An account that doesn't exist is no error.
func DropUser(site contract.SiteName) Statement {
	const kind = "drop user"
	if err := site.Validate(); err != nil {
		return invalid(kind, "the site name", err)
	}
	return Statement{kind: kind, sql: "DROP USER IF EXISTS " + accounts(site) + ";"}
}

// globalPrivilegeColumns are the privilege columns of mysql.user in MySQL
// 8.4. A new account has 'N' in all of them; a 'Y' in any is a global
// privilege the site's user must not hold. A real-server test compares this
// list with the table's columns.
var globalPrivilegeColumns = []string{
	"Select_priv", "Insert_priv", "Update_priv", "Delete_priv", "Create_priv", "Drop_priv", "Reload_priv",
	"Shutdown_priv", "Process_priv", "File_priv", "Grant_priv", "References_priv", "Index_priv", "Alter_priv",
	"Show_db_priv", "Super_priv", "Create_tmp_table_priv", "Lock_tables_priv", "Execute_priv", "Repl_slave_priv",
	"Repl_client_priv", "Create_view_priv", "Show_view_priv", "Create_routine_priv", "Alter_routine_priv",
	"Create_user_priv", "Event_priv", "Trigger_priv", "Create_tablespace_priv", "Create_role_priv", "Drop_role_priv",
}

// Kinds of Privilege.
const (
	PrivGlobal      = "global"       // a global privilege (a 'Y' in mysql.user)
	PrivDynamic     = "dynamic"      // a dynamic global privilege (mysql.global_grants)
	PrivRole        = "role"         // a role granted to the account
	PrivProxy       = "proxy"        // the right to proxy another account
	PrivDatabase    = "database"     // a row of mysql.db
	PrivTable       = "table"        // a row of mysql.tables_priv
	PrivRoutine     = "routine"      // a row of mysql.procs_priv
	PrivColumn      = "column"       // a row of mysql.columns_priv
	PrivGrantOption = "grant option" // the right to pass a privilege on
)

// SiteAccountPrivileges selects everything the site's two accounts hold
// beyond a locked account with no privileges: global and dynamic
// privileges, roles, proxy rights, and rows for databases, tables, routines
// and columns, each with the database it is for. The caller decides which
// of the database rows are the site's own.
func SiteAccountPrivileges(site contract.SiteName) Statement {
	const kind = "list account privileges"
	if err := site.Validate(); err != nil {
		return invalid(kind, "the site name", err)
	}
	user := "'s_" + string(site) + "'"
	hosts := "('localhost', '127.0.0.1')"
	who := func(userCol, hostCol string) string {
		return " WHERE " + userCol + " = " + user + " AND " + hostCol + " IN " + hosts
	}
	return Statement{kind: kind, sql: "SELECT 'global' AS kind, User, Host, '' AS object FROM mysql.user" + who("User", "Host") + " AND 'Y' IN (" + strings.Join(globalPrivilegeColumns, ", ") + ")" +
		" UNION ALL SELECT 'dynamic', USER, HOST, PRIV FROM mysql.global_grants" + who("USER", "HOST") +
		" UNION ALL SELECT 'role', TO_USER, TO_HOST, FROM_USER FROM mysql.role_edges" + who("TO_USER", "TO_HOST") +
		" UNION ALL SELECT 'proxy', User, Host, Proxied_user FROM mysql.proxies_priv" + who("User", "Host") +
		" UNION ALL SELECT 'database', User, Host, Db FROM mysql.db" + who("User", "Host") +
		" UNION ALL SELECT 'grant option', User, Host, Db FROM mysql.db" + who("User", "Host") + " AND Grant_priv = 'Y'" +
		" UNION ALL SELECT 'table', User, Host, Db FROM mysql.tables_priv" + who("User", "Host") +
		" UNION ALL SELECT 'grant option', User, Host, Db FROM mysql.tables_priv" + who("User", "Host") + " AND FIND_IN_SET('Grant', Table_priv)" +
		" UNION ALL SELECT 'routine', User, Host, Db FROM mysql.procs_priv" + who("User", "Host") +
		" UNION ALL SELECT 'grant option', User, Host, Db FROM mysql.procs_priv" + who("User", "Host") + " AND FIND_IN_SET('Grant', Proc_priv)" +
		" UNION ALL SELECT 'column', User, Host, Db FROM mysql.columns_priv" + who("User", "Host") +
		" ORDER BY 1, 2, 3, 4;"}
}

// Privilege is one thing a site's account holds: its Kind (one of the Priv
// constants), the account, and for rows of a database, table, routine or
// column grant the database it names, as MySQL stores it.
type Privilege struct {
	Kind, User, Host, Object string
}

// AccountPrivileges returns what the site's accounts hold; a clean pair of
// accounts hold nothing, and an empty list is returned.
func AccountPrivileges(ctx context.Context, d sysexec.Deps, site contract.SiteName) ([]Privilege, error) {
	rows, err := Exec(ctx, d, SiteAccountPrivileges(site))
	if err != nil {
		return nil, err
	}
	var out []Privilege
	for _, r := range rows {
		if len(r) != 4 {
			return nil, fmt.Errorf("MySQL answered %q to the query for an account's privileges", strings.Join(r, "\t"))
		}
		out = append(out, Privilege{Kind: r[0], User: r[1], Host: r[2], Object: r[3]})
	}
	return out, nil
}
