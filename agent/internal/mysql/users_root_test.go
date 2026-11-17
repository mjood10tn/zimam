//go:build roottest

package mysql_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"zimam/agent/contract"
	"zimam/agent/internal/commands"
	"zimam/agent/internal/jobs"
	"zimam/agent/internal/mysql"
	"zimam/agent/internal/paths"
	"zimam/agent/internal/sites"
	"zimam/agent/internal/sysexec"
)

// These tests need a throwaway machine with root and ZIMAM_ROOTTEST_MYSQL=1:
// they install MySQL, and make and drop sites' state, users and databases.

// userEnv is a root test machine with the agent's handlers wired to the real
// MySQL.
type userEnv struct {
	t   *testing.T
	ctx context.Context
	p   paths.Paths
	d   sysexec.Deps
	j   *jobs.Context
}

func newUserEnv(t *testing.T) *userEnv {
	t.Helper()
	if os.Getenv("ZIMAM_ROOTTEST_MYSQL") != "1" {
		t.Skip("set ZIMAM_ROOTTEST_MYSQL=1 to install MySQL on this machine")
	}
	if os.Geteuid() != 0 {
		t.Fatal("the roottest tests need root")
	}
	p := paths.Paths{}
	d := sysexec.Deps{Paths: p, Run: sysexec.ExecRunner{}, Logf: t.Logf, Now: time.Now}
	if _, err := mysql.Install(context.Background(), d); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if err := os.MkdirAll(p.LocksDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	return &userEnv{t: t, ctx: context.Background(), p: p, d: d,
		j: &jobs.Context{ID: "01JBX7Q3A1B2C3D4E5F6G7H8J9", Logf: t.Logf, Paths: p, Run: sysexec.ExecRunner{}}}
}

func (e *userEnv) run(command string, params contract.Params) error {
	e.t.Helper()
	_, err := commands.JobHandlers()[command](e.ctx, e.j, params)
	return err
}

func (e *userEnv) newSite(name contract.SiteName) {
	e.t.Helper()
	st := sites.State{Name: name, Domain: contract.Domain(string(name) + ".example.com"), Type: contract.SiteStatic, TemplateVersion: 1, Security: sites.DefaultSecurity()}
	if err := sites.Save(e.p, st); err != nil {
		e.t.Fatal(err)
	}
}

func (e *userEnv) databases(name contract.SiteName) []contract.DatabaseName {
	e.t.Helper()
	st, err := sites.Load(e.p, name)
	if err != nil {
		e.t.Fatal(err)
	}
	return st.Databases
}

// wipe removes what a test leaves: its sites' state, users and databases.
func (e *userEnv) wipe(names ...contract.SiteName) {
	for _, n := range names {
		prefix := contract.DatabasePrefix(n)
		for _, db := range []string{"one", "two", "old"} {
			exec.Command("/usr/bin/mysql", "--protocol=socket", "--user=root", "--execute=DROP DATABASE IF EXISTS "+prefix+db).Run()
		}
		mysql.Exec(e.ctx, e.d, mysql.DropUser(n))
		os.Remove(e.p.SiteState(n))
	}
}

func rootOut(t *testing.T, sql string) string {
	t.Helper()
	out, err := exec.Command("/usr/bin/mysql", "--protocol=socket", "--user=root", "--batch", "--skip-column-names", "--execute="+sql).CombinedOutput()
	if err != nil {
		t.Fatalf("%s: %v\n%s", sql, err, out)
	}
	return strings.TrimSpace(string(out))
}

// loginAs runs a statement as a site's user over the socket or over TCP to
// 127.0.0.1, with the password in the environment, never in argv.
func loginAs(user string, pw contract.ServicePassword, tcp bool, sql string) (string, error) {
	args := []string{"--user=" + user, "--batch", "--skip-column-names", "--execute=" + sql}
	if tcp {
		args = append(args, "--protocol=tcp", "--host=127.0.0.1", "--port=3306")
	} else {
		args = append(args, "--protocol=socket")
	}
	cmd := exec.Command("/usr/bin/mysql", args...)
	cmd.Env = append(os.Environ(), "MYSQL_PWD="+string(pw))
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func (e *userEnv) mustReach(user string, pw contract.ServicePassword, sql string) {
	e.t.Helper()
	for _, tcp := range []bool{false, true} {
		if out, err := loginAs(user, pw, tcp, sql); err != nil {
			e.t.Errorf("%s (tcp %v) can't run %q: %v\n%s", user, tcp, sql, err, out)
		}
	}
}

// mustBeDenied expects the statement to fail with one of the MySQL errors,
// over the socket and over TCP.
func (e *userEnv) mustBeDenied(user string, pw contract.ServicePassword, sql string, errnos ...int) {
	e.t.Helper()
	for _, tcp := range []bool{false, true} {
		out, err := loginAs(user, pw, tcp, sql)
		ok := false
		for _, n := range errnos {
			ok = ok || strings.Contains(out, fmt.Sprintf("ERROR %d", n))
		}
		if err == nil || !ok {
			e.t.Errorf("%s (tcp %v) running %q: %v\n%s\nwant one of ERROR %v", user, tcp, sql, err, out, errnos)
		}
	}
}

// The user has the grants of its own databases only, nothing global, and
// cannot reach another site's database over the socket or over TCP.
func TestSiteUserCannotReachOtherSitesDatabase(t *testing.T) {
	e := newUserEnv(t)
	e.wipe("ua", "ub")
	t.Cleanup(func() { e.wipe("ua", "ub") })
	e.newSite("ua")
	e.newSite("ub")
	pwA, pwB := contract.ServicePassword("Aa1_aaaaaaaaaaaaaaaaaaaaaaaaaaaa-A"), contract.ServicePassword("Bb2-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbb_B")

	// A database made before the user exists, and one after: both end up
	// granted.
	for site, db := range map[contract.SiteName]contract.DatabaseName{"ua": "s_ua_one", "ub": "s_ub_one"} {
		if err := e.run("db.create", &contract.DbCreateParams{Site: site, Database: db}); err != nil {
			t.Fatal(err)
		}
		rootSQL(t, "CREATE TABLE "+string(db)+".secret (v INT); INSERT INTO "+string(db)+".secret VALUES (42)")
	}
	for site, pw := range map[contract.SiteName]contract.ServicePassword{"ua": pwA, "ub": pwB} {
		if err := e.run("db.user", &contract.DbUserParams{Site: site, Password: pw}); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.run("db.create", &contract.DbCreateParams{Site: "ua", Database: "s_ua_two"}); err != nil {
		t.Fatal(err)
	}

	e.requireOnlyDatabaseGrants("s_ua", "s_ua_one", "s_ua_two")

	e.mustReach("s_ua", pwA, "SELECT v FROM s_ua_one.secret")
	e.mustReach("s_ua", pwA, "CREATE TABLE IF NOT EXISTS s_ua_two.t (id INT)")
	e.mustReach("s_ub", pwB, "SELECT v FROM s_ub_one.secret")
	e.mustBeDenied("s_ua", pwA, "SELECT v FROM s_ub_one.secret", 1142, 1044)
	e.mustBeDenied("s_ub", pwB, "SELECT v FROM s_ua_one.secret", 1142, 1044)
	e.mustBeDenied("s_ua", pwA, "CREATE DATABASE s_ub_two", 1044)
	e.mustBeDenied("s_ua", pwA, "SELECT user FROM mysql.user", 1142, 1044)
	e.mustBeDenied("s_ua", pwA, "SELECT 1 INTO OUTFILE '/var/lib/mysql-files/x'", 1227)
	e.mustBeDenied("s_ua", pwB, "SELECT 1", 1045) // a's user with b's password
}

// Rotating the password refuses the old one and accepts the new one on both
// accounts, and the grants stay.
func TestRotatePasswordAgainstRealMySQL(t *testing.T) {
	e := newUserEnv(t)
	e.wipe("ua")
	t.Cleanup(func() { e.wipe("ua") })
	e.newSite("ua")
	pw1, pw2 := contract.ServicePassword("Aa1_aaaaaaaaaaaaaaaaaaaaaaaaaaaa-A"), contract.ServicePassword("Cc3-cccccccccccccccccccccccccccccc_C")
	if err := e.run("db.create", &contract.DbCreateParams{Site: "ua", Database: "s_ua_one"}); err != nil {
		t.Fatal(err)
	}
	if err := e.run("db.user", &contract.DbUserParams{Site: "ua", Password: pw1}); err != nil {
		t.Fatal(err)
	}
	rootSQL(t, "CREATE TABLE s_ua_one.t (id INT)")
	e.mustReach("s_ua", pw1, "SELECT COUNT(*) FROM s_ua_one.t")
	grantsBefore := rootOut(t, "SHOW GRANTS FOR 's_ua'@'localhost'") + rootOut(t, "SHOW GRANTS FOR 's_ua'@'127.0.0.1'")

	// Running it again with the same password is fine too.
	if err := e.run("db.user", &contract.DbUserParams{Site: "ua", Password: pw1}); err != nil {
		t.Fatal(err)
	}
	if err := e.run("db.user", &contract.DbUserParams{Site: "ua", Password: pw2}); err != nil {
		t.Fatal(err)
	}
	e.mustBeDenied("s_ua", pw1, "SELECT 1", 1045)
	e.mustReach("s_ua", pw2, "SELECT COUNT(*) FROM s_ua_one.t")
	if got := rootOut(t, "SHOW GRANTS FOR 's_ua'@'localhost'") + rootOut(t, "SHOW GRANTS FOR 's_ua'@'127.0.0.1'"); got != grantsBefore {
		t.Errorf("grants changed by a rotation:\n%s\nwas\n%s", got, grantsBefore)
	}
	if st, err := sites.Load(e.p, "ua"); err != nil || !st.DBUser {
		t.Errorf("state: %+v, %v", st, err)
	}
}

// A site with a MySQL user and databases is destroyed only with its
// databases; then its databases and both accounts are gone, with every
// privilege row, so a site made later under the same name starts empty.
func TestDestroyAgainstRealMySQL(t *testing.T) {
	e := newUserEnv(t)
	e.wipe("ua")
	t.Cleanup(func() { e.wipe("ua") })
	e.newSite("ua")
	pw1, pw2 := contract.ServicePassword("Aa1_aaaaaaaaaaaaaaaaaaaaaaaaaaaa-A"), contract.ServicePassword("Dd4-dddddddddddddddddddddddddddddd_D")
	for _, db := range []contract.DatabaseName{"s_ua_one", "s_ua_two"} {
		if err := e.run("db.create", &contract.DbCreateParams{Site: "ua", Database: db}); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.run("db.user", &contract.DbUserParams{Site: "ua", Password: pw1}); err != nil {
		t.Fatal(err)
	}
	// A grant written the other way, as someone could by hand: dropping the
	// accounts must take every row.
	rootSQL(t, "GRANT SELECT ON `s_ua_old`.* TO 's_ua'@'localhost'")
	if n := rootOut(t, "SELECT COUNT(*) FROM mysql.db WHERE User = 's_ua'"); n != "5" {
		t.Fatalf("mysql.db holds %s rows for s_ua before the destroy, want 5", n)
	}

	// Without drop_databases: refused, nothing changed.
	err := e.run("site.destroy", &contract.SiteDestroyParams{Name: "ua"})
	var ae *contract.AgentError
	if !errors.As(err, &ae) || ae.Code != contract.ErrConflict || !strings.Contains(ae.Message, "s_ua_one") {
		t.Fatalf("destroy without drop_databases: %v", err)
	}
	if got := e.databases("ua"); len(got) != 2 {
		t.Errorf("databases after the refusal: %v", got)
	}
	e.mustReach("s_ua", pw1, "SELECT 1")
	if rootOut(t, "SHOW DATABASES LIKE 's\\_ua\\_%'") == "" {
		t.Error("the databases are gone after a refused destroy")
	}

	// With it.
	if err := e.run("site.destroy", &contract.SiteDestroyParams{Name: "ua", DropDatabases: true}); err != nil {
		t.Fatalf("destroy: %v", err)
	}
	if out := rootOut(t, "SHOW DATABASES LIKE 's\\_ua\\_%'"); out != "" {
		t.Errorf("databases left: %s", out)
	}
	if n := rootOut(t, "SELECT COUNT(*) FROM mysql.user WHERE User = 's_ua'"); n != "0" {
		t.Errorf("%s accounts left", n)
	}
	if n := rootOut(t, "SELECT COUNT(*) FROM mysql.db WHERE User = 's_ua'"); n != "0" {
		t.Errorf("%s privilege rows left in mysql.db", n)
	}
	e.mustBeDenied("s_ua", pw1, "SELECT 1", 1045, 1130)
	if _, err := sites.Load(e.p, "ua"); err == nil {
		t.Error("the state file is left")
	}

	// The same name again: no user, nothing granted, until db.user runs, and
	// then only the grants of the new site's own databases (none).
	e.newSite("ua")
	if out, err := exec.Command("/usr/bin/mysql", "--protocol=socket", "--user=root", "--execute=SHOW GRANTS FOR 's_ua'@'localhost'").CombinedOutput(); err == nil || !strings.Contains(string(out), "ERROR 1141") {
		t.Errorf("SHOW GRANTS for the recreated site's user: %v\n%s", err, out)
	}
	rootSQL(t, "CREATE DATABASE s_ua_one; CREATE TABLE s_ua_one.t (id INT)") // someone's database under the old name
	if err := e.run("db.user", &contract.DbUserParams{Site: "ua", Password: pw2}); err != nil {
		t.Fatal(err)
	}
	for _, host := range []string{"localhost", "127.0.0.1"} {
		if grants := rootOut(t, "SHOW GRANTS FOR 's_ua'@'"+host+"'"); strings.Contains(grants, "\n") || !strings.HasPrefix(grants, "GRANT USAGE ON *.*") {
			t.Errorf("grants of the recreated site's %s account:\n%s", host, grants)
		}
	}
	e.mustBeDenied("s_ua", pw2, "SELECT * FROM s_ua_one.t", 1142, 1044)
	e.mustBeDenied("s_ua", pw2, "SELECT * FROM s_ua_old.t", 1142, 1044)
	rootSQL(t, "DROP DATABASE s_ua_one")
	if err := e.run("site.destroy", &contract.SiteDestroyParams{Name: "ua"}); err != nil {
		t.Fatalf("destroy of the recreated site: %v", err)
	}
}

// The grantee query finds an escaped grant whatever the server's sql_mode:
// with NO_BACKSLASH_ESCAPES on, a query that relied on the server's mode
// would stop matching the backslashes in the stored pattern.
func TestGranteesUnderNoBackslashEscapes(t *testing.T) {
	e := newUserEnv(t)
	e.wipe("ua")
	t.Cleanup(func() { e.wipe("ua") })
	e.newSite("ua")
	pw := contract.ServicePassword("Aa1_aaaaaaaaaaaaaaaaaaaaaaaaaaaa-A")
	if err := e.run("db.create", &contract.DbCreateParams{Site: "ua", Database: "s_ua_one"}); err != nil {
		t.Fatal(err)
	}
	if err := e.run("db.user", &contract.DbUserParams{Site: "ua", Password: pw}); err != nil {
		t.Fatal(err)
	}
	original := rootOut(t, "SELECT @@GLOBAL.sql_mode")
	t.Cleanup(func() { rootSQL(t, "SET GLOBAL sql_mode = '"+original+"'") })
	rootSQL(t, "SET GLOBAL sql_mode = CONCAT_WS(',', NULLIF(@@GLOBAL.sql_mode, ''), 'NO_BACKSLASH_ESCAPES')")
	t.Logf("global sql_mode now: %s", rootOut(t, "SELECT @@GLOBAL.sql_mode"))
	// What a query that keeps the server's mode sees.
	t.Logf("a query that keeps the server's mode finds %s rows", rootOut(t, "SELECT COUNT(*) FROM mysql.db WHERE 's_ua_one' LIKE Db AND User = 's_ua'"))

	got, err := mysql.Grantees(e.ctx, e.d, "s_ua_one")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Pattern != `s\_ua\_one` || got[1].Pattern != `s\_ua\_one` {
		t.Errorf("grantees under NO_BACKSLASH_ESCAPES: %v, want both accounts with the escaped pattern", got)
	}
}

// requireOnlyDatabaseGrants checks both accounts of a site: USAGE at the
// global level, ALL PRIVILEGES on each of the databases, nothing else, and no
// global privilege flag in mysql.user.
func (e *userEnv) requireOnlyDatabaseGrants(user string, dbs ...string) {
	e.t.Helper()
	for _, host := range []string{"localhost", "127.0.0.1"} {
		grants := rootOut(e.t, "SHOW GRANTS FOR '"+user+"'@'"+host+"'")
		e.t.Logf("SHOW GRANTS FOR '%s'@'%s':\n%s", user, host, grants)
		lines := strings.Split(grants, "\n")
		if len(lines) != 1+len(dbs) || !strings.HasPrefix(lines[0], "GRANT USAGE ON *.* TO `"+user+"`@`"+host+"`") {
			e.t.Errorf("%s: want a global USAGE and %d database grants, got:\n%s", host, len(dbs), grants)
		}
		for _, l := range lines {
			if strings.Contains(l, "GRANT OPTION") || (strings.Contains(l, " ON *.*") && !strings.HasPrefix(l, "GRANT USAGE ON *.*")) {
				e.t.Errorf("%s: a global or grantable privilege: %s", host, l)
			}
		}
		for _, db := range dbs {
			// --batch prints a backslash as two.
			want := "GRANT ALL PRIVILEGES ON `" + strings.ReplaceAll(db, "_", `\\_`) + "`.* TO `" + user + "`@`" + host + "`"
			if !strings.Contains(grants, want) {
				e.t.Errorf("%s: no %s in\n%s", host, want, grants)
			}
		}
	}
	if n := rootOut(e.t, "SELECT COUNT(*) FROM mysql.user WHERE User = '"+user+"' AND (File_priv = 'Y' OR Process_priv = 'Y' OR Super_priv = 'Y' OR Grant_priv = 'Y' OR Create_user_priv = 'Y' OR Reload_priv = 'Y' OR Shutdown_priv = 'Y' OR Select_priv = 'Y' OR Create_priv = 'Y')"); n != "0" {
		e.t.Errorf("a global privilege flag is set on %s: %s", user, n)
	}
}

// Whatever order the commands run in, and after a rotation, a site's user
// holds USAGE globally and database-level grants on its own databases only.
func TestNoGlobalPrivileges(t *testing.T) {
	e := newUserEnv(t)
	e.wipe("ua")
	t.Cleanup(func() { e.wipe("ua") })
	e.newSite("ua")
	pw1, pw2 := contract.ServicePassword("Aa1_aaaaaaaaaaaaaaaaaaaaaaaaaaaa-A"), contract.ServicePassword("Ee5-eeeeeeeeeeeeeeeeeeeeeeeeeeeeee_E")
	if err := e.run("db.create", &contract.DbCreateParams{Site: "ua", Database: "s_ua_one"}); err != nil {
		t.Fatal(err)
	}
	if err := e.run("db.user", &contract.DbUserParams{Site: "ua", Password: pw1}); err != nil {
		t.Fatal(err)
	}
	if err := e.run("db.create", &contract.DbCreateParams{Site: "ua", Database: "s_ua_two"}); err != nil {
		t.Fatal(err)
	}
	e.requireOnlyDatabaseGrants("s_ua", "s_ua_one", "s_ua_two")
	if err := e.run("db.user", &contract.DbUserParams{Site: "ua", Password: pw2}); err != nil {
		t.Fatal(err)
	}
	e.requireOnlyDatabaseGrants("s_ua", "s_ua_one", "s_ua_two")
}
