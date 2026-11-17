package mysql

import "zimam/agent/contract"

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
