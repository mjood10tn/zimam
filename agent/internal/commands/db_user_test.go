package commands_test

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	"zimam/agent/contract"
	"zimam/agent/internal/commands"
	"zimam/agent/internal/jobs"
	"zimam/agent/internal/sysexec"
)

const (
	pwOne = contract.ServicePassword("Qk3_wL9-pT7vXz2RmN4sYb8D")
	pwTwo = contract.ServicePassword("Zr5-hC1_uJ6eGa0VfK9tOq3W")
)

func userParams(site contract.SiteName, pw contract.ServicePassword) *contract.DbUserParams {
	return &contract.DbUserParams{Site: site, Password: pw}
}

// shopWithDatabases is a server where shop has two databases.
func shopWithDatabases(t *testing.T) *dbEnv {
	t.Helper()
	e := newDBEnv(t)
	st := e.site(t, "shop")
	st.Databases = []contract.DatabaseName{"s_shop_a", "s_shop_b"}
	e.saveSite(t, st)
	return e
}

const both = "'s_shop'@'localhost', 's_shop'@'127.0.0.1'"

func wantUserSQL(pw contract.ServicePassword) []string {
	by := " IDENTIFIED BY '" + string(pw) + "'"
	accounts := "'s_shop'@'localhost'" + by + ", 's_shop'@'127.0.0.1'" + by
	return []string{
		"CREATE USER IF NOT EXISTS " + accounts + ";",
		"ALTER USER " + accounts + ";",
		"GRANT ALL PRIVILEGES ON `s" + `\_shop\_a` + "`.* TO " + both + ";",
		"GRANT ALL PRIVILEGES ON `s" + `\_shop\_b` + "`.* TO " + both + ";",
	}
}

func equalSQL(t *testing.T, got, want []string) {
	t.Helper()
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("SQL\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

func TestDbUserWired(t *testing.T) {
	d, ok := contract.Lookup("db.user")
	if !ok || d.Kind != contract.KindJob || !d.Audited {
		t.Fatalf("db.user = %+v, %v; want an audited job", d, ok)
	}
	if got := d.Subject(userParams("shop", pwOne)); got != "site:shop" {
		t.Errorf("subject %q", got)
	}
	if l, ok := jobs.LimitsFor("db.user"); !ok || l != (jobs.Limits{MemoryMax: "256M", RuntimeMaxSec: 120}) {
		t.Errorf("limits %+v", l)
	}
	if commands.JobHandlers()["db.user"] == nil || commands.Prechecks(sysexec.Deps{})["db.user"] == nil {
		t.Error("db.user has no handler or precheck")
	}
}

// The user gets a grant on each of the site's databases and nothing global,
// and the state records that the site has a user.
func TestUserCreatedWithGrantsOnOwnDatabasesOnly(t *testing.T) {
	e := shopWithDatabases(t)
	for range 4 {
		e.answer("", "")
	}
	res, err := e.job(t, "db.user", userParams("shop", pwOne))
	if err != nil {
		t.Fatal(err)
	}
	if res != (contract.ServiceUserResult{Site: "shop"}) {
		t.Errorf("result %+v", res)
	}
	got := e.sql()
	equalSQL(t, got, wantUserSQL(pwOne))
	for _, s := range got {
		if strings.Contains(s, "GRANT OPTION") || strings.Contains(s, "ON *.*") || strings.Contains(s, "FILE") {
			t.Errorf("a global or grantable privilege: %s", s)
		}
	}
	if st := e.site(t, "shop"); !st.DBUser || len(st.Databases) != 2 {
		t.Errorf("state %+v", st)
	}
	if st := e.site(t, "blog"); st.DBUser {
		t.Error("another site got a user")
	}
}

func TestUserForASiteWithoutDatabases(t *testing.T) {
	e := newDBEnv(t)
	e.answer("", "")
	e.answer("", "")
	if _, err := e.job(t, "db.user", userParams("blog", pwOne)); err != nil {
		t.Fatal(err)
	}
	got := e.sql()
	if len(got) != 2 || !strings.HasPrefix(got[0], "CREATE USER IF NOT EXISTS") || !strings.HasPrefix(got[1], "ALTER USER") {
		t.Errorf("SQL = %q", got)
	}
	if !e.site(t, "blog").DBUser {
		t.Error("db_user not set")
	}
}

// Rotating is the same command on a site that has the user: the password is
// set again, the grants are applied again (they are idempotent) and the state
// file doesn't change.
func TestRotateKeepsGrants(t *testing.T) {
	e := shopWithDatabases(t)
	for range 8 {
		e.answer("", "")
	}
	if _, err := e.job(t, "db.user", userParams("shop", pwOne)); err != nil {
		t.Fatal(err)
	}
	before := e.stateFile(t, "shop")
	if _, err := e.job(t, "db.user", userParams("shop", pwTwo)); err != nil {
		t.Fatal(err)
	}
	equalSQL(t, e.sql()[4:], wantUserSQL(pwTwo))
	if got := e.stateFile(t, "shop"); got != before {
		t.Errorf("state changed by a rotation:\n%s", got)
	}
}

// The failure points, each with what it leaves. The password is also
// checked in every error.
func TestDbUserFailurePoints(t *testing.T) {
	const fail = "ERROR 1396 (HY000) at line 1: Operation failed"
	for name, tc := range map[string]struct {
		step     int  // the statement that fails, from 0
		wantRuns int  // statements run
		dbUser   bool // db_user in the state afterwards
	}{
		"create fails":       {0, 1, false},
		"alter fails":        {1, 2, false},
		"first grant fails":  {2, 3, true},
		"second grant fails": {3, 4, true},
	} {
		t.Run(name, func(t *testing.T) {
			e := shopWithDatabases(t)
			for i := range 4 {
				if i == tc.step {
					e.answer("", fail)
				} else {
					e.answer("", "")
				}
			}
			var logs []string
			j := &jobs.Context{ID: "01JBX7Q3A1B2C3D4E5F6G7H8J9", Logf: func(f string, a ...any) { logs = append(logs, fmt.Sprintf(f, a...)) }, Paths: e.p, Run: e.run}
			_, err := commands.JobHandlers()["db.user"](context.Background(), j, userParams("shop", pwOne))
			if err == nil {
				t.Fatal("no error")
			}
			if strings.Contains(err.Error(), string(pwOne)) {
				t.Errorf("the password is in the error: %v", err)
			}
			// A later step is never run after a failure.
			if got := e.sql(); len(got) != tc.wantRuns {
				t.Errorf("%d statements, want %d", len(got), tc.wantRuns)
			}
			if got := e.site(t, "shop").DBUser; got != tc.dbUser {
				t.Errorf("db_user = %v, want %v", got, tc.dbUser)
			}
		})
	}
}

// A rotation that fails at the password statement changes nothing: no grant
// is run, the state stays as it was, and the old password keeps working.
func TestRotationFailingAtThePasswordChangesNothingElse(t *testing.T) {
	e := shopWithDatabases(t)
	st := e.site(t, "shop")
	st.DBUser = true
	e.saveSite(t, st)
	before := e.stateFile(t, "shop")
	e.answer("", "")
	e.answer("", "ERROR 1396 (HY000) at line 1: Operation failed")
	if _, err := e.job(t, "db.user", userParams("shop", pwTwo)); err == nil {
		t.Fatal("no error")
	}
	for _, s := range e.sql() {
		if strings.HasPrefix(s, "GRANT") {
			t.Errorf("a grant ran after the password failed: %s", s)
		}
	}
	if e.stateFile(t, "shop") != before {
		t.Error("state changed")
	}
}

// The state file can't be saved once the accounts exist: the error says the
// accounts exist, and no grant runs. Running the command again
// converges.
func TestDbUserStateSaveFailureSaysRunAgain(t *testing.T) {
	e := shopWithDatabases(t)
	e.run = sysexec.NewFakeRunner()
	hook := &hookRunner{Runner: e.run, after: 2, do: breakState(t, e, "shop")}
	for range 4 {
		e.run.Add(sysexec.Script{Path: mysqlClient, Args: mysqlClientArgs})
	}
	j := &jobs.Context{ID: "01JBX7Q3A1B2C3D4E5F6G7H8J9", Logf: t.Logf, Paths: e.p, Run: hook}
	_, err := commands.JobHandlers()["db.user"](context.Background(), j, userParams("shop", pwOne))
	if err == nil || !strings.Contains(err.Error(), "run the command again") || strings.Contains(err.Error(), string(pwOne)) {
		t.Fatalf("err = %v", err)
	}
	if n := len(e.run.Calls()); n != 2 {
		t.Errorf("%d statements ran after the state could not be saved", n)
	}
}

func TestDbUserWhileMySQLDownOrMissing(t *testing.T) {
	e := shopWithDatabases(t)
	before := e.stateFile(t, "shop")
	e.answer("", errDown)
	ae := wantCode(t, mustFail(e.job(t, "db.user", userParams("shop", pwOne))), contract.ErrConflict)
	if !strings.Contains(ae.Message, "isn't running") {
		t.Errorf("message %q", ae.Message)
	}
	if e.stateFile(t, "shop") != before {
		t.Error("state changed while MySQL was down")
	}
	wantCode(t, e.precheck(t, "db.user", userParams("nope", pwOne)), contract.ErrNotFound)
	removeServices(t, e)
	e.run = sysexec.NewFakeRunner()
	e.d.Run = e.run
	for _, err := range []error{e.precheck(t, "db.user", userParams("shop", pwOne)), mustFail(e.job(t, "db.user", userParams("shop", pwOne)))} {
		ae := wantCode(t, err, contract.ErrConflict)
		if !strings.Contains(ae.Message, "isn't installed") {
			t.Errorf("message %q", ae.Message)
		}
	}
	if len(e.run.Calls()) != 0 {
		t.Error("the client ran with MySQL not installed")
	}
}

// The password reaches the client only on stdin. It is in no argument, no
// environment variable, no log line, no error and no state file, on success
// and on each failure.
func TestPasswordNeverInJobLogOrArgv(t *testing.T) {
	const fail = "ERROR 1396 (HY000) at line 1: Operation failed"
	for name, failAt := range map[string]int{"success": -1, "create fails": 0, "alter fails": 1, "grant fails": 2} {
		t.Run(name, func(t *testing.T) {
			e := shopWithDatabases(t)
			for i := range 4 {
				if i == failAt {
					e.answer("", fail)
				} else {
					e.answer("", "")
				}
			}
			var logs []string
			j := &jobs.Context{ID: "01JBX7Q3A1B2C3D4E5F6G7H8J9", Logf: func(f string, a ...any) { logs = append(logs, fmt.Sprintf(f, a...)) }, Paths: e.p, Run: e.run}
			res, err := commands.JobHandlers()["db.user"](context.Background(), j, userParams("shop", pwOne))
			if (err != nil) != (failAt >= 0) {
				t.Fatalf("err = %v", err)
			}
			if failAt < 0 && len(logs) == 0 {
				t.Error("the job logged nothing, so the search of the log proves nothing")
			}
			var seen []string
			seen = append(seen, logs...)
			if err != nil {
				seen = append(seen, err.Error(), fmt.Sprintf("%#v %+v", err, err))
			}
			seen = append(seen, fmt.Sprintf("%v %+v %#v", res, res, res))
			for _, c := range e.run.Calls() {
				seen = append(seen, c.Path, strings.Join(c.Args, " "), strings.Join(c.Env, " "), c.Dir)
			}
			seen = append(seen, e.stateFile(t, "shop"))
			for _, s := range seen {
				if strings.Contains(s, string(pwOne)) {
					t.Errorf("the password is in %q", s)
				}
			}
			// It does go to the client, on stdin, so the search above looks for the right text.
			if !strings.Contains(e.sql()[0], string(pwOne)) {
				t.Error("the password never reached the client's stdin")
			}
		})
	}
}

// db.user then db.create, and db.create then db.user: either way the new
// database is granted to the site's user.
func TestUserThenCreateGrantsTheNewDatabase(t *testing.T) {
	e := newDBEnv(t)
	for range 8 {
		e.answer("", "")
	}
	if _, err := e.job(t, "db.user", userParams("blog", pwOne)); err != nil {
		t.Fatal(err)
	}
	if _, err := e.job(t, "db.create", create("blog", "s_blog_a")); err != nil {
		t.Fatal(err)
	}
	got := e.sql()
	want := "GRANT ALL PRIVILEGES ON `s" + `\_blog\_a` + "`.* TO 's_blog'@'localhost', 's_blog'@'127.0.0.1';"
	if got[len(got)-1] != want {
		t.Errorf("last statement %q, want %q", got[len(got)-1], want)
	}
}

func TestCreateThenUserGrantsTheExistingDatabase(t *testing.T) {
	e := newDBEnv(t)
	for range 8 {
		e.answer("", "")
	}
	if _, err := e.job(t, "db.create", create("blog", "s_blog_a")); err != nil {
		t.Fatal(err)
	}
	n := len(e.sql())
	if _, err := e.job(t, "db.user", userParams("blog", pwOne)); err != nil {
		t.Fatal(err)
	}
	var grants int
	for _, s := range e.sql()[n:] {
		if strings.HasPrefix(s, "GRANT ALL PRIVILEGES ON `s"+`\_blog\_a`+"`.*") {
			grants++
		}
	}
	if grants != 1 {
		t.Errorf("%d grants on the existing database: %q", grants, e.sql()[n:])
	}
}

func mustFail(_ any, err error) error { return err }

func removeServices(t *testing.T, e *dbEnv) {
	t.Helper()
	if err := os.Remove(e.p.ServicesState()); err != nil {
		t.Fatal(err)
	}
}

// breakState makes the site's state file impossible to replace.
func breakState(t *testing.T, e *dbEnv, name contract.SiteName) func() {
	return func() {
		if err := os.Remove(e.p.SiteState(name)); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(e.p.SiteState(name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
}
