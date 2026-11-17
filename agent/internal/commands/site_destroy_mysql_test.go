package commands_test

import (
	"os"
	"strings"
	"testing"

	"zimam/agent/contract"
)

// withLocks gives a test server the lock directory a destroy needs.
func withLocks(t *testing.T, e *dbEnv) *dbEnv {
	t.Helper()
	if err := os.MkdirAll(e.p.LocksDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	return e
}

func destroyParams(drop bool) *contract.SiteDestroyParams {
	return &contract.SiteDestroyParams{Name: "shop", DropDatabases: drop}
}

func shopWithUser(t *testing.T, databases ...contract.DatabaseName) *dbEnv {
	t.Helper()
	e := newDBEnv(t)
	st := e.site(t, "shop")
	st.Databases = databases
	st.DBUser = true
	e.saveSite(t, st)
	return withLocks(t, e)
}

func stateExists(e *dbEnv, name contract.SiteName) bool {
	_, err := os.Stat(e.p.SiteState(name))
	return err == nil
}

// A site that still has databases isn't destroyed unless the databases go
// too: the answer is a conflict that names them, before anything changes, and
// whether MySQL runs or is even installed.
func TestDestroyRefusesWithDatabases(t *testing.T) {
	for name, setup := range map[string]func(*dbEnv){
		"running":       func(*dbEnv) {},
		"not installed": func(e *dbEnv) { removeServices(t, e) },
	} {
		t.Run(name, func(t *testing.T) {
			e := shopWithUser(t, "s_shop_a", "s_shop_b")
			setup(e)
			before := e.stateFile(t, "shop")
			for _, err := range []error{e.precheck(t, "site.destroy", destroyParams(false)), mustFail(e.job(t, "site.destroy", destroyParams(false)))} {
				ae := wantCode(t, err, contract.ErrConflict)
				if !strings.Contains(ae.Message, "s_shop_a") || !strings.Contains(ae.Message, "s_shop_b") {
					t.Errorf("message %q doesn't name the databases", ae.Message)
				}
			}
			if n := len(e.run.Calls()); n != 0 {
				t.Errorf("%d runs", n)
			}
			if e.stateFile(t, "shop") != before {
				t.Error("state changed")
			}
		})
	}
}

// With drop_databases, each database is revoked then dropped, and the user,
// both accounts, goes after them; the site's own files go last.
func TestDestroyDropsDatabasesWhenAsked(t *testing.T) {
	e := shopWithUser(t, "s_shop_a", "s_shop_b")
	for range 5 {
		e.answer("", "")
	}
	if err := e.precheck(t, "site.destroy", destroyParams(true)); err != nil {
		t.Fatal(err)
	}
	if _, err := e.job(t, "site.destroy", destroyParams(true)); err != nil {
		t.Fatal(err)
	}
	var mysqlRuns []string
	for _, c := range e.run.Calls() {
		if c.Path == mysqlClient {
			mysqlRuns = append(mysqlRuns, strings.TrimSpace(string(c.Stdin)))
		}
	}
	equalSQL(t, mysqlRuns, []string{
		"REVOKE IF EXISTS ALL PRIVILEGES ON `s" + `\_shop\_a` + "`.* FROM " + both + " IGNORE UNKNOWN USER;",
		"DROP DATABASE IF EXISTS `s_shop_a`;",
		"REVOKE IF EXISTS ALL PRIVILEGES ON `s" + `\_shop\_b` + "`.* FROM " + both + " IGNORE UNKNOWN USER;",
		"DROP DATABASE IF EXISTS `s_shop_b`;",
		"DROP USER IF EXISTS " + both + ";",
	})
	if stateExists(e, "shop") {
		t.Error("the state file is left")
	}
}

func TestDestroyDropsUser(t *testing.T) {
	e := shopWithUser(t)
	e.answer("", "")
	if _, err := e.job(t, "site.destroy", destroyParams(false)); err != nil {
		t.Fatal(err)
	}
	equalSQL(t, e.sql(), []string{"DROP USER IF EXISTS " + both + ";"})
	if stateExists(e, "shop") {
		t.Error("the state file is left")
	}
}

// A server that never installed MySQL destroys a site as before. A site that
// lists nothing in MySQL still has its accounts dropped when MySQL is
// installed (an orphan from a failed state save would otherwise stay), but
// never fails the destroy: MySQL down or the statement failing is only logged.
func TestDestroyTidiesTheAccountsOfASiteWithNothingListed(t *testing.T) {
	t.Run("installed", func(t *testing.T) {
		e := withLocks(t, newDBEnv(t))
		e.answer("", "")
		if err := e.precheck(t, "site.destroy", destroyParams(false)); err != nil {
			t.Fatal(err)
		}
		if _, err := e.job(t, "site.destroy", destroyParams(true)); err != nil {
			t.Fatal(err)
		}
		equalSQL(t, e.sql(), []string{"DROP USER IF EXISTS " + both + ";"})
		if stateExists(e, "shop") {
			t.Error("the state file is left")
		}
	})
	for name, stderr := range map[string]string{"down": errDown, "failing": "ERROR 1396 (HY000) at line 1: Operation failed"} {
		t.Run("installed but "+name, func(t *testing.T) {
			e := withLocks(t, newDBEnv(t))
			e.answer("", stderr)
			if err := e.precheck(t, "site.destroy", destroyParams(false)); err != nil {
				t.Fatal(err)
			}
			if _, err := e.job(t, "site.destroy", destroyParams(false)); err != nil {
				t.Fatalf("the destroy failed for a site that lists nothing in MySQL: %v", err)
			}
			if stateExists(e, "shop") {
				t.Error("the state file is left")
			}
		})
	}
	t.Run("not installed", func(t *testing.T) {
		e := withLocks(t, newDBEnv(t))
		removeServices(t, e)
		if err := e.precheck(t, "site.destroy", destroyParams(false)); err != nil {
			t.Fatal(err)
		}
		if _, err := e.job(t, "site.destroy", destroyParams(true)); err != nil {
			t.Fatal(err)
		}
		for _, c := range e.run.Calls() {
			if c.Path == mysqlClient {
				t.Error("the MySQL client ran")
			}
		}
		if stateExists(e, "shop") {
			t.Error("the state file is left")
		}
	})
}

// The state lists databases but not a user: the databases go and the accounts
// are dropped all the same.
func TestDestroyDropsTheUserEvenWhenTheStateHasNone(t *testing.T) {
	e := withLocks(t, newDBEnv(t))
	st := e.site(t, "shop")
	st.Databases = []contract.DatabaseName{"s_shop_a"}
	e.saveSite(t, st)
	for range 3 {
		e.answer("", "")
	}
	if _, err := e.job(t, "site.destroy", destroyParams(true)); err != nil {
		t.Fatal(err)
	}
	equalSQL(t, e.sql(), []string{
		"REVOKE IF EXISTS ALL PRIVILEGES ON `s" + `\_shop\_a` + "`.* FROM " + both + " IGNORE UNKNOWN USER;",
		"DROP DATABASE IF EXISTS `s_shop_a`;",
		"DROP USER IF EXISTS " + both + ";",
	})
}

// DROP USER failing leaves the databases dropped, the site whole and the state
// saying it still has a user; running the destroy again finishes it.
func TestDestroyDropUserFailureLeavesAWorkingSite(t *testing.T) {
	e := shopWithUser(t, "s_shop_a")
	e.answer("", "")
	e.answer("", "")
	e.answer("", "ERROR 1396 (HY000) at line 1: Operation failed")
	_, err := e.job(t, "site.destroy", destroyParams(true))
	if err == nil || !strings.Contains(err.Error(), "drop user") {
		t.Fatalf("err = %v", err)
	}
	st := e.site(t, "shop")
	if len(st.Databases) != 0 || !st.DBUser {
		t.Errorf("state: databases %v, db_user %v", st.Databases, st.DBUser)
	}
	for _, c := range e.run.Calls() {
		if c.Path != mysqlClient {
			t.Errorf("%s ran before the user was dropped", c.Path)
		}
	}
	// Again: nothing is left to drop but the user.
	e.answer("", "")
	if _, err := e.job(t, "site.destroy", destroyParams(false)); err != nil {
		t.Fatal(err)
	}
	if stateExists(e, "shop") {
		t.Error("the state file is left")
	}
}

// MySQL that isn't running or isn't installed stops the destroy before
// anything changes, when the site has something in it.
func TestDestroyNeedsMySQLForASiteThatUsesIt(t *testing.T) {
	t.Run("down", func(t *testing.T) {
		e := shopWithUser(t, "s_shop_a")
		before := e.stateFile(t, "shop")
		e.answer("", errDown)
		ae := wantCode(t, mustFail(e.job(t, "site.destroy", destroyParams(true))), contract.ErrConflict)
		if !strings.Contains(ae.Message, "isn't running") {
			t.Errorf("message %q", ae.Message)
		}
		if e.stateFile(t, "shop") != before {
			t.Error("state changed")
		}
		if n := len(e.run.Calls()); n != 1 {
			t.Errorf("%d runs after MySQL didn't answer", n)
		}
	})
	t.Run("user only, not installed", func(t *testing.T) {
		e := shopWithUser(t)
		removeServices(t, e)
		for _, err := range []error{e.precheck(t, "site.destroy", destroyParams(false)), mustFail(e.job(t, "site.destroy", destroyParams(false)))} {
			ae := wantCode(t, err, contract.ErrConflict)
			if !strings.Contains(ae.Message, "isn't installed") {
				t.Errorf("message %q", ae.Message)
			}
		}
		if !stateExists(e, "shop") || len(e.run.Calls()) != 0 {
			t.Error("something changed")
		}
	})
}

// A failure midway leaves the state true to what remains, and the site is
// still whole, so running the destroy again finishes it.
func TestDestroyFailureMidwayKeepsAWorkingSite(t *testing.T) {
	e := shopWithUser(t, "s_shop_a", "s_shop_b")
	e.answer("", "")                                               // revoke a
	e.answer("", "")                                               // drop a
	e.answer("", "ERROR 1396 (HY000) at line 1: Operation failed") // revoke b
	if _, err := e.job(t, "site.destroy", destroyParams(true)); err == nil {
		t.Fatal("no error")
	}
	st := e.site(t, "shop")
	if len(st.Databases) != 1 || st.Databases[0] != "s_shop_b" || !st.DBUser {
		t.Errorf("state: databases %v, db_user %v", st.Databases, st.DBUser)
	}
	for _, c := range e.run.Calls() {
		if c.Path != mysqlClient {
			t.Errorf("%s ran before the databases were dropped", c.Path)
		}
	}
	// Again, with MySQL answering.

	e.answer("", "")
	if _, err := e.job(t, "site.destroy", destroyParams(true)); err != nil {
		t.Fatal(err)
	}
	if stateExists(e, "shop") {
		t.Error("the state file is left")
	}
}
