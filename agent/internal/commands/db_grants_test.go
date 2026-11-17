package commands_test

import (
	"strings"
	"testing"

	"zimam/agent/contract"
)

// batchRow is a row of the grantee query as the client prints it: tab
// separated, a backslash written as two.
func batchRow(user, host, pattern string) string {
	return batchRowOfKind("database", user, host, pattern)
}

// batchRowOfKind is batchRow for a row of another grant table.
func batchRowOfKind(kind, user, host, pattern string) string {
	return kind + "\t" + user + "\t" + host + "\t" + strings.ReplaceAll(pattern, `\`, `\\`) + "\n"
}

// takeoverEnv is a server where blog (with a MySQL user) takes over the
// empty database s_blog_a, which no site lists. grantees is what the first
// grantee query answers.
func takeoverEnv(t *testing.T, grantees string) *dbEnv {
	t.Helper()
	e := newDBEnv(t)
	st := e.site(t, "blog")
	st.DBUser = true
	e.saveSite(t, st)
	e.answer("", errExists)
	e.answer("0\n", "")
	e.answer(grantees, "")
	return e
}

func prefixes(t *testing.T, got []string, want ...string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%d statements, want %d: %q", len(got), len(want), got)
	}
	for i, w := range want {
		if !strings.HasPrefix(got[i], w) {
			t.Errorf("statement %d = %q, want it to start with %q", i, got[i], w)
		}
	}
}

// A site's account left with a grant on the name, say by a failed cleanup, is
// revoked, and the takeover goes on with the new site's own grant.
func TestTakeoverRevokesAnotherSitesLeftoverGrant(t *testing.T) {
	e := takeoverEnv(t, batchRow("s_shop", "localhost", `s\_blog\_a`)+batchRow("s_shop", "127.0.0.1", `s\_blog\_a`))
	e.answer("", "") // the revoke
	e.answer("", "") // the grantee query again: nobody
	e.answer("", "") // the site's own grant
	if _, err := e.job(t, "db.create", create("blog", "s_blog_a")); err != nil {
		t.Fatal(err)
	}
	got := e.sql()
	prefixes(t, got, "CREATE DATABASE", "SELECT (SELECT COUNT(*)", "SET SESSION sql_mode", "REVOKE IF EXISTS ALL PRIVILEGES ON `s", "SET SESSION sql_mode", "GRANT ALL PRIVILEGES ON `s")
	wantRevoke := "REVOKE IF EXISTS ALL PRIVILEGES ON `s" + `\_blog\_a` + "`.* FROM 's_shop'@'localhost', 's_shop'@'127.0.0.1' IGNORE UNKNOWN USER;"
	if got[3] != wantRevoke {
		t.Errorf("revoke = %s\nwant     %s", got[3], wantRevoke)
	}
	if !strings.Contains(got[5], "TO 's_blog'@'localhost', 's_blog'@'127.0.0.1'") {
		t.Errorf("grant = %s", got[5])
	}
	if dbs := e.site(t, "blog").Databases; len(dbs) != 1 || dbs[0] != "s_blog_a" {
		t.Errorf("databases %v", dbs)
	}
}

// A grant written with the name as it is has a row the escaped REVOKE would
// not match, so it is revoked as written.
func TestTakeoverRevokesAGrantWrittenWithTheNameAsItIs(t *testing.T) {
	e := takeoverEnv(t, batchRow("s_shop", "localhost", "s_blog_a"))
	e.answer("", "")
	e.answer("", "")
	e.answer("", "")
	if _, err := e.job(t, "db.create", create("blog", "s_blog_a")); err != nil {
		t.Fatal(err)
	}
	got := e.sql()
	prefixes(t, got, "CREATE DATABASE", "SELECT (SELECT", "SET SESSION sql_mode", "REVOKE IF EXISTS ALL PRIVILEGES ON `s_blog_a`.* FROM 's_shop'@", "SET SESSION sql_mode", "GRANT")
}

// Anything but a site's own account on this exact name refuses the
// takeover, and nothing but the questions has run.
func TestTakeoverIsRefusedForOtherGrants(t *testing.T) {
	for name, tc := range map[string]struct{ row, account string }{
		"another user":                {batchRow("hand", "localhost", `s\_blog\_a`), "'hand'@'localhost'"},
		"a user of another host":      {batchRow("s_shop", "%", `s\_blog\_a`), "'s_shop'@'%'"},
		"a host that is no site's":    {batchRow("s_shop", "10.0.0.1", `s\_blog\_a`), "'s_shop'@'10.0.0.1'"},
		"a wildcard pattern":          {batchRow("s_shop", "localhost", `s\_blog\_%`), "'s_shop'@'localhost'"},
		"a pattern with a percent":    {batchRow("s_blog", "localhost", `%`), "'s_blog'@'localhost'"},
		"a user that is not a site's": {batchRow("s_Bad`;x", "localhost", `s\_blog\_a`), "'s_Bad`;x'@'localhost'"},
		"a user with only the prefix": {batchRow("s_", "localhost", `s\_blog\_a`), "'s_'@'localhost'"},
		"root":                        {batchRow("root", "localhost", `s\_blog\_a`), "'root'@'localhost'"},
	} {
		e := takeoverEnv(t, tc.row)
		before := e.stateFile(t, "blog")
		_, err := e.job(t, "db.create", create("blog", "s_blog_a"))
		ae := wantCode(t, err, contract.ErrConflict)
		if !strings.Contains(ae.Message, tc.account) || !strings.Contains(ae.Message, "did not give") {
			t.Errorf("%s: message %q", name, ae.Message)
		}
		// Only the create, the count and the grantee query ran: nothing
		// from the column text reached a statement.
		prefixes(t, e.sql(), "CREATE DATABASE", "SELECT (SELECT", "SET SESSION sql_mode")
		if e.stateFile(t, "blog") != before {
			t.Errorf("%s: state changed", name)
		}
	}
}

// Two kinds of account at once: the leftover is cleared first, and the
// foreign one still refuses.
func TestTakeoverRefusesWhatRemainsAfterTheRevokes(t *testing.T) {
	e := takeoverEnv(t, batchRow("s_shop", "localhost", `s\_blog\_a`))
	e.answer("", "")
	e.answer(batchRow("hand", "localhost", `s\_blog\_a`), "") // still there
	_, err := e.job(t, "db.create", create("blog", "s_blog_a"))
	ae := wantCode(t, err, contract.ErrConflict)
	if !strings.Contains(ae.Message, "'hand'@'localhost'") {
		t.Errorf("message %q", ae.Message)
	}
	prefixes(t, e.sql(), "CREATE DATABASE", "SELECT (SELECT", "SET SESSION sql_mode", "REVOKE", "SET SESSION sql_mode")
	if len(e.site(t, "blog").Databases) != 0 {
		t.Error("the database was listed")
	}
}

// A database that was just created can't carry another account's grant
// either (a grant outlives its database). It is refused and dropped again.
func TestFreshCreateRefusedForAForeignGrantDropsTheNewDatabase(t *testing.T) {
	e := newDBEnv(t)
	st := e.site(t, "blog")
	st.DBUser = true
	e.saveSite(t, st)
	e.answer("", "")
	e.answer(batchRow("hand", "localhost", `s\_blog\_a`), "")
	e.answer("", "")
	e.answer("", "")
	before := e.stateFile(t, "blog")
	_, err := e.job(t, "db.create", create("blog", "s_blog_a"))
	wantCode(t, err, contract.ErrConflict)
	prefixes(t, e.sql(), "CREATE DATABASE", "SET SESSION sql_mode", "REVOKE IF EXISTS", "DROP DATABASE IF EXISTS `s_blog_a`")
	if e.stateFile(t, "blog") != before {
		t.Error("state changed")
	}
}

// A database that existed before is not Zimam's to drop: when the grant or
// the save fails after a takeover, only the grant is revoked.
func TestFailedGrantAfterATakeoverLeavesTheExistingDatabase(t *testing.T) {
	e := takeoverEnv(t, "")
	e.answer("", "ERROR 1396 (HY000) at line 1: Operation GRANT failed")
	e.answer("", "") // the revoke
	before := e.stateFile(t, "blog")
	if _, err := e.job(t, "db.create", create("blog", "s_blog_a")); err == nil {
		t.Fatal("no error")
	}
	got := e.sql()
	prefixes(t, got, "CREATE DATABASE", "SELECT (SELECT", "SET SESSION sql_mode", "GRANT", "REVOKE IF EXISTS")
	for _, s := range got {
		if strings.Contains(s, "DROP DATABASE") {
			t.Errorf("the existing database was dropped: %q", got)
		}
	}
	if e.stateFile(t, "blog") != before {
		t.Error("state changed")
	}
}

func TestFailedUndoAfterATakeoverIsInTheRecordedMessage(t *testing.T) {
	e := takeoverEnv(t, "")
	e.answer("", "ERROR 2013 (HY000): Lost connection to MySQL server")
	e.answer("", "ERROR 2013 (HY000): Lost connection to MySQL server")
	_, err := e.job(t, "db.create", create("blog", "s_blog_a"))
	ae := wantCode(t, err, contract.ErrConflict)
	for _, want := range []string{"isn't running", "may remain", "creating it again will clear it"} {
		if !strings.Contains(ae.Message, want) {
			t.Errorf("message %q lacks %q", ae.Message, want)
		}
	}
}

// Zimam makes no grant on a table, routine or column, so one that exists
// was made by hand: it refuses the takeover, even on a site's own account,
// and nothing is revoked.
func TestTakeoverRefusesGrantsBelowTheDatabase(t *testing.T) {
	for _, kind := range []string{mysqlPrivTable, "routine", "column"} {
		e := takeoverEnv(t, batchRow("s_shop", "localhost", `s\_blog\_a`)+batchRowOfKind(kind, "s_shop", "127.0.0.1", "s_blog_a"))
		before := e.stateFile(t, "blog")
		_, err := e.job(t, "db.create", create("blog", "s_blog_a"))
		ae := wantCode(t, err, contract.ErrConflict)
		if !strings.Contains(ae.Message, "'s_shop'@'127.0.0.1'") || !strings.Contains(ae.Message, "a "+kind+" grant in s_blog_a") {
			t.Errorf("%s: message %q", kind, ae.Message)
		}
		prefixes(t, e.sql(), "CREATE DATABASE", "SELECT (SELECT", "SET SESSION sql_mode")
		if e.stateFile(t, "blog") != before {
			t.Errorf("%s: state changed", kind)
		}
	}
}

const mysqlPrivTable = "table"
