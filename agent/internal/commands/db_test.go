package commands_test

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"zimam/agent/contract"
	"zimam/agent/internal/commands"
	"zimam/agent/internal/jobs"
	"zimam/agent/internal/paths"
	"zimam/agent/internal/services"
	"zimam/agent/internal/sites"
	"zimam/agent/internal/sysexec"
)

const (
	mysqlClient = "/usr/bin/mysql"
	errDown     = "ERROR 2002 (HY000): Can't connect to local MySQL server through socket '/var/run/mysqld/mysqld.sock' (2)"
)

var mysqlClientArgs = []string{"--protocol=socket", "--user=root", "--batch", "--skip-column-names"}

// dbEnv is a server with MySQL installed and the sites shop, shop-x and
// blog, none with a database.
type dbEnv struct {
	p   paths.Paths
	run *sysexec.FakeRunner
	d   sysexec.Deps
}

func newDBEnv(t *testing.T) *dbEnv {
	t.Helper()
	p := paths.Paths{Root: t.TempDir()}
	run := sysexec.NewFakeRunner()
	e := &dbEnv{p: p, run: run, d: sysexec.Deps{Paths: p, Run: run, Logf: t.Logf, Now: time.Now}}
	if err := services.Update(p, func(s *services.State) { s.MySQL = services.Entry{Installed: true, Version: "8.4.13"} }); err != nil {
		t.Fatal(err)
	}
	for _, name := range []contract.SiteName{"shop", "shop-x", "blog"} {
		e.saveSite(t, sites.State{Name: name, Domain: contract.Domain(string(name) + ".example.com"), Type: contract.SiteStatic, TemplateVersion: 1, Security: sites.DefaultSecurity()})
	}
	return e
}

func (e *dbEnv) saveSite(t *testing.T, st sites.State) {
	t.Helper()
	if err := sites.Save(e.p, st); err != nil {
		t.Fatal(err)
	}
}

func (e *dbEnv) site(t *testing.T, name contract.SiteName) sites.State {
	t.Helper()
	st, err := sites.Load(e.p, name)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

func (e *dbEnv) stateFile(t *testing.T, name contract.SiteName) string {
	t.Helper()
	b, err := os.ReadFile(e.p.SiteState(name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// answer scripts the next client run: its output, or an error (exit code 1
// with this stderr) when stderr is set.
func (e *dbEnv) answer(stdout, stderr string) {
	s := sysexec.Script{Path: mysqlClient, Args: mysqlClientArgs, Result: sysexec.Result{Stdout: []byte(stdout)}}
	if stderr != "" {
		s.Result = sysexec.Result{ExitCode: 1, Stderr: []byte(stderr)}
	}
	e.run.Add(s)
}

// sql is what each client run got on stdin, in order.
func (e *dbEnv) sql() []string {
	var out []string
	for _, c := range e.run.Calls() {
		out = append(out, strings.TrimSpace(string(c.Stdin)))
	}
	return out
}

func (e *dbEnv) job(t *testing.T, command string, p contract.Params) (any, error) {
	t.Helper()
	h := commands.JobHandlers()[command]
	if h == nil {
		t.Fatalf("%s has no job handler", command)
	}
	return h(context.Background(), &jobs.Context{ID: "01JBX7Q3A1B2C3D4E5F6G7H8J9", Logf: t.Logf, Paths: e.p, Run: e.run}, p)
}

func (e *dbEnv) precheck(t *testing.T, command string, p contract.Params) error {
	t.Helper()
	pre := commands.Prechecks(e.d)[command]
	if pre == nil {
		t.Fatalf("%s has no precheck", command)
	}
	return pre(context.Background(), p)
}

func (e *dbEnv) list(t *testing.T, site contract.SiteName) (any, error) {
	t.Helper()
	h := commands.New(e.d, nil)["db.list"]
	if h == nil {
		t.Fatal("db.list has no handler")
	}
	return h(context.Background(), &contract.DbListParams{Site: site})
}

func create(site contract.SiteName, db contract.DatabaseName) *contract.DbCreateParams {
	return &contract.DbCreateParams{Site: site, Database: db}
}

func drop(site contract.SiteName, db contract.DatabaseName) *contract.DbDropParams {
	return &contract.DbDropParams{Site: site, Database: db}
}

func TestDbCommandsWired(t *testing.T) {
	for command, limits := range map[string]jobs.Limits{
		"db.create": {MemoryMax: "256M", RuntimeMaxSec: 120},
		"db.drop":   {MemoryMax: "256M", RuntimeMaxSec: 300},
	} {
		d, ok := contract.Lookup(command)
		if !ok || d.Kind != contract.KindJob || !d.Audited {
			t.Fatalf("%s = %+v, %v; want an audited job", command, d, ok)
		}
		if l, ok := jobs.LimitsFor(command); !ok || l != limits {
			t.Errorf("%s limits %+v", command, l)
		}
		if commands.JobHandlers()[command] == nil {
			t.Errorf("%s has no job handler", command)
		}
		if commands.Prechecks(sysexec.Deps{})[command] == nil {
			t.Errorf("%s has no precheck", command)
		}
	}
	if d, ok := contract.Lookup("db.list"); !ok || d.Kind != contract.KindRead {
		t.Errorf("db.list = %+v, %v", d, ok)
	}
}

func TestCreateRefusesNameOfOtherSite(t *testing.T) {
	e := newDBEnv(t)
	// shop-x's own database is also covered by shop's prefix.
	st := e.site(t, "shop-x")
	st.Databases = []contract.DatabaseName{"s_shop_x_a"}
	e.saveSite(t, st)
	before := e.stateFile(t, "shop")

	for name, tc := range map[string]struct {
		site contract.SiteName
		db   contract.DatabaseName
		want string
	}{
		"listed by the other site":           {"shop", "s_shop_x_a", "belongs to site shop-x"},
		"covered by the other site's prefix": {"shop", "s_shop_x_b", "prefix of site shop-x"},
		"listed by the site itself":          {"shop-x", "s_shop_x_a", "already has"},
	} {
		e.run = sysexec.NewFakeRunner()
		e.d.Run = e.run
		for _, err := range []error{e.precheck(t, "db.create", create(tc.site, tc.db)), func() error { _, err := e.job(t, "db.create", create(tc.site, tc.db)); return err }()} {
			ae := wantCode(t, err, contract.ErrConflict)
			if !strings.Contains(ae.Message, tc.want) {
				t.Errorf("%s: message %q lacks %q", name, ae.Message, tc.want)
			}
		}
		if n := len(e.run.Calls()); n != 0 {
			t.Errorf("%s: %d MySQL runs", name, n)
		}
	}
	if got := e.stateFile(t, "shop"); got != before {
		t.Errorf("shop's state changed:\n%s", got)
	}
}

func TestCreateWithoutUser(t *testing.T) {
	e := newDBEnv(t)
	st := e.site(t, "blog")
	st.Databases = []contract.DatabaseName{"s_blog_b"}
	e.saveSite(t, st)
	e.answer("", "")
	res, err := e.job(t, "db.create", create("blog", "s_blog_a"))
	if err != nil {
		t.Fatal(err)
	}
	if res != (contract.DatabaseResult{Site: "blog", Database: "s_blog_a"}) {
		t.Errorf("result %+v", res)
	}
	if got := e.sql(); len(got) != 1 || got[0] != "CREATE DATABASE `s_blog_a` CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci;" {
		t.Errorf("SQL = %q", got)
	}
	got := e.site(t, "blog")
	if len(got.Databases) != 2 || got.Databases[0] != "s_blog_a" || got.Databases[1] != "s_blog_b" || got.DBUser {
		t.Errorf("state: databases %v, db_user %v; want both, sorted, no user", got.Databases, got.DBUser)
	}
}

func TestCreateGrantsToExistingUser(t *testing.T) {
	e := newDBEnv(t)
	st := e.site(t, "blog")
	st.DBUser = true
	e.saveSite(t, st)
	e.answer("", "")
	e.answer("", "")
	if _, err := e.job(t, "db.create", create("blog", "s_blog_main")); err != nil {
		t.Fatal(err)
	}
	got := e.sql()
	if len(got) != 2 || !strings.HasPrefix(got[0], "CREATE DATABASE `s_blog_main`") {
		t.Fatalf("SQL = %q", got)
	}
	wantGrant := "GRANT ALL PRIVILEGES ON `s" + `\_blog\_main` + "`.* TO 's_blog'@'localhost', 's_blog'@'127.0.0.1';"
	if got[1] != wantGrant {
		t.Errorf("grant = %s\nwant    %s", got[1], wantGrant)
	}
	if st := e.site(t, "blog"); len(st.Databases) != 1 || st.Databases[0] != "s_blog_main" || !st.DBUser {
		t.Errorf("state %+v", st)
	}
}

func TestCreateWhileMySQLDownChangesNothing(t *testing.T) {
	e := newDBEnv(t)
	before := e.stateFile(t, "blog")
	e.answer("", errDown)
	_, err := e.job(t, "db.create", create("blog", "s_blog_a"))
	ae := wantCode(t, err, contract.ErrConflict)
	if !strings.Contains(ae.Message, "isn't running") {
		t.Errorf("message %q", ae.Message)
	}
	if got := e.stateFile(t, "blog"); got != before {
		t.Errorf("state changed:\n%s", got)
	}
	if got := e.sql(); len(got) != 1 {
		t.Errorf("SQL after a failed connect: %q", got)
	}
}

func TestCreateWithoutMySQLInstalled(t *testing.T) {
	e := newDBEnv(t)
	if err := os.Remove(e.p.ServicesState()); err != nil {
		t.Fatal(err)
	}
	before := e.stateFile(t, "blog")
	for _, err := range []error{e.precheck(t, "db.create", create("blog", "s_blog_a")), func() error { _, err := e.job(t, "db.create", create("blog", "s_blog_a")); return err }()} {
		ae := wantCode(t, err, contract.ErrConflict)
		if !strings.Contains(ae.Message, "isn't installed") {
			t.Errorf("message %q", ae.Message)
		}
	}
	if len(e.run.Calls()) != 0 || e.stateFile(t, "blog") != before {
		t.Error("something changed")
	}
}

func TestCreateUnknownSite(t *testing.T) {
	e := newDBEnv(t)
	wantCode(t, e.precheck(t, "db.create", create("nope", "s_nope_a")), contract.ErrNotFound)
}

func TestCreateUndoesTheDatabaseWhenTheGrantFails(t *testing.T) {
	e := newDBEnv(t)
	st := e.site(t, "blog")
	st.DBUser = true
	e.saveSite(t, st)
	before := e.stateFile(t, "blog")
	e.answer("", "")
	e.answer("", "ERROR 1396 (HY000) at line 1: Operation GRANT failed")
	e.answer("", "")
	e.answer("", "")
	_, err := e.job(t, "db.create", create("blog", "s_blog_a"))
	if err == nil {
		t.Fatal("no error")
	}
	got := e.sql()
	if len(got) != 4 || !strings.HasPrefix(got[1], "GRANT ") || !strings.HasPrefix(got[2], "REVOKE IF EXISTS") || got[3] != "DROP DATABASE IF EXISTS `s_blog_a`;" {
		t.Fatalf("SQL = %q", got)
	}
	if e.stateFile(t, "blog") != before {
		t.Error("state changed")
	}
}

// A state file that can't be saved leaves no database behind.
func TestCreateUndoesTheDatabaseWhenTheStateCannotBeSaved(t *testing.T) {
	e := newDBEnv(t)
	st := e.site(t, "blog")
	st.DBUser = true
	e.saveSite(t, st)
	// Once the grant has run, make the state file impossible to replace.
	e.run = sysexec.NewFakeRunner()
	hook := &hookRunner{Runner: e.run, after: 2, do: func() {
		if err := os.Remove(e.p.SiteState("blog")); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(e.p.SiteState("blog"), 0o700); err != nil {
			t.Fatal(err)
		}
	}}
	for range 4 {
		e.run.Add(sysexec.Script{Path: mysqlClient, Args: mysqlClientArgs})
	}
	j := &jobs.Context{ID: "01JBX7Q3A1B2C3D4E5F6G7H8J9", Logf: t.Logf, Paths: e.p, Run: hook}
	_, err := commands.JobHandlers()["db.create"](context.Background(), j, create("blog", "s_blog_a"))
	if err == nil || !strings.Contains(err.Error(), "saving site blog's state") {
		t.Fatalf("err = %v", err)
	}
	var stmts []string
	for _, c := range e.run.Calls() {
		stmts = append(stmts, strings.TrimSpace(string(c.Stdin)))
	}
	if len(stmts) != 4 || !strings.HasPrefix(stmts[0], "CREATE DATABASE") || !strings.HasPrefix(stmts[1], "GRANT") ||
		!strings.HasPrefix(stmts[2], "REVOKE IF EXISTS") || !strings.HasPrefix(stmts[3], "DROP DATABASE IF EXISTS `s_blog_a`") {
		t.Errorf("SQL = %q", stmts)
	}
}

// hookRunner runs do once, after the given number of runs.
type hookRunner struct {
	sysexec.Runner
	after, n int
	do       func()
}

func (h *hookRunner) Run(ctx context.Context, c sysexec.Cmd) (sysexec.Result, error) {
	res, err := h.Runner.Run(ctx, c)
	if h.n++; h.n == h.after {
		h.do()
	}
	return res, err
}

func TestDropOtherSitesDatabaseIsNotFound(t *testing.T) {
	e := newDBEnv(t)
	st := e.site(t, "blog")
	st.Databases = []contract.DatabaseName{"s_blog_a"}
	e.saveSite(t, st)
	shop := e.site(t, "shop")
	shop.Databases = []contract.DatabaseName{"s_shop_a"}
	e.saveSite(t, shop)
	shopx := e.site(t, "shop-x")
	shopx.Databases = []contract.DatabaseName{"s_shop_x_a"}
	e.saveSite(t, shopx)
	for name, tc := range map[string]struct {
		site contract.SiteName
		db   contract.DatabaseName
	}{
		"the name of another site's database": {"shop", "s_blog_a"},
		"a name no site lists":                {"blog", "s_blog_untracked"},
		"another site's under its own prefix": {"shop-x", "s_shop_a"},
		// valid under shop's prefix too, but shop-x lists it
		"a name a longer-prefixed site lists": {"shop", "s_shop_x_a"},
	} {
		for _, err := range []error{e.precheck(t, "db.drop", drop(tc.site, tc.db)), func() error { _, err := e.job(t, "db.drop", drop(tc.site, tc.db)); return err }()} {
			if err == nil {
				t.Fatalf("%s: no error", name)
			}
			wantCode(t, err, contract.ErrNotFound)
		}
	}
	if n := len(e.run.Calls()); n != 0 {
		t.Errorf("%d MySQL runs", n)
	}
	wantCode(t, e.precheck(t, "db.drop", drop("nope", "s_nope_a")), contract.ErrNotFound)
}

func TestDropRevokesThenDrops(t *testing.T) {
	for _, hasUser := range []bool{true, false} {
		e := newDBEnv(t)
		st := e.site(t, "shop-x")
		st.Databases = []contract.DatabaseName{"s_shop_x_a", "s_shop_x_b"}
		st.DBUser = hasUser
		e.saveSite(t, st)
		e.answer("", "")
		e.answer("", "")
		res, err := e.job(t, "db.drop", drop("shop-x", "s_shop_x_a"))
		if err != nil {
			t.Fatal(err)
		}
		if res != (contract.DatabaseResult{Site: "shop-x", Database: "s_shop_x_a"}) {
			t.Errorf("result %+v", res)
		}
		got := e.sql()
		wantRevoke := "REVOKE IF EXISTS ALL PRIVILEGES ON `s" + `\_shop\_x\_a` + "`.* FROM 's_shop-x'@'localhost', 's_shop-x'@'127.0.0.1' IGNORE UNKNOWN USER;"
		if len(got) != 2 || got[0] != wantRevoke || got[1] != "DROP DATABASE IF EXISTS `s_shop_x_a`;" {
			t.Errorf("hasUser=%v: SQL = %q", hasUser, got)
		}
		if after := e.site(t, "shop-x"); len(after.Databases) != 1 || after.Databases[0] != "s_shop_x_b" || after.DBUser != hasUser {
			t.Errorf("state %+v", after)
		}
	}
}

// Dropping the last database leaves the state without the field, as a site
// that never had one.
func TestDropLastDatabaseLeavesNoField(t *testing.T) {
	e := newDBEnv(t)
	plain := e.stateFile(t, "blog")
	st := e.site(t, "blog")
	st.Databases = []contract.DatabaseName{"s_blog_a"}
	e.saveSite(t, st)
	e.answer("", "")
	e.answer("", "")
	if _, err := e.job(t, "db.drop", drop("blog", "s_blog_a")); err != nil {
		t.Fatal(err)
	}
	if got := e.stateFile(t, "blog"); got != plain {
		t.Errorf("state after the last drop:\n%s", got)
	}
}

// A drop whose state save failed left the database dropped and the state
// listing it. Running the command again finishes: both statements are the
// forms that accept a database that is already gone.
func TestDropConvergesAfterAFailedStateSave(t *testing.T) {
	e := newDBEnv(t)
	st := e.site(t, "blog")
	st.Databases = []contract.DatabaseName{"s_blog_a"}
	e.saveSite(t, st)
	listed := e.stateFile(t, "blog")
	run := sysexec.NewFakeRunner()
	for range 4 {
		run.Add(sysexec.Script{Path: mysqlClient, Args: mysqlClientArgs})
	}
	e.run = run
	hook := &hookRunner{Runner: run, after: 2, do: func() {
		if err := os.Remove(e.p.SiteState("blog")); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(e.p.SiteState("blog"), 0o700); err != nil {
			t.Fatal(err)
		}
	}}
	h := commands.JobHandlers()["db.drop"]
	_, err := h(context.Background(), &jobs.Context{ID: "01JBX7Q3A1B2C3D4E5F6G7H8J9", Logf: t.Logf, Paths: e.p, Run: hook}, drop("blog", "s_blog_a"))
	if err == nil || !strings.Contains(err.Error(), "run the command again") {
		t.Fatalf("err = %v", err)
	}
	// The state file comes back as it was; the second run finds the
	// database already gone.
	if err := os.Remove(e.p.SiteState("blog")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(e.p.SiteState("blog"), []byte(listed), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := e.job(t, "db.drop", drop("blog", "s_blog_a")); err != nil {
		t.Fatal(err)
	}
	if got := e.site(t, "blog"); len(got.Databases) != 0 {
		t.Errorf("databases %v", got.Databases)
	}
	got := e.sql()
	if len(got) != 4 || !strings.Contains(got[2], "REVOKE IF EXISTS") || !strings.Contains(got[2], "IGNORE UNKNOWN USER") || got[3] != "DROP DATABASE IF EXISTS `s_blog_a`;" {
		t.Errorf("SQL = %q", got)
	}
}

// REVOKE worked and DROP DATABASE failed: the state keeps the database, and
// the message says what to do.
func TestDropFailureAfterRevokeSaysRunAgain(t *testing.T) {
	e := newDBEnv(t)
	st := e.site(t, "blog")
	st.Databases = []contract.DatabaseName{"s_blog_a"}
	e.saveSite(t, st)
	before := e.stateFile(t, "blog")
	e.answer("", "")
	e.answer("", "ERROR 1010 (HY000): Error dropping database")
	_, err := e.job(t, "db.drop", drop("blog", "s_blog_a"))
	if err == nil || !strings.Contains(err.Error(), "run the command again") || !strings.Contains(err.Error(), "no longer has access") {
		t.Fatalf("err = %v", err)
	}
	if e.stateFile(t, "blog") != before {
		t.Error("state changed")
	}
}

func TestDropFailureKeepsTheStateAndStopsBeforeTheDrop(t *testing.T) {
	e := newDBEnv(t)
	st := e.site(t, "blog")
	st.Databases = []contract.DatabaseName{"s_blog_a"}
	e.saveSite(t, st)
	before := e.stateFile(t, "blog")

	e.answer("", "ERROR 1227 (42000): Access denied")
	if _, err := e.job(t, "db.drop", drop("blog", "s_blog_a")); err == nil {
		t.Fatal("no error")
	}
	if got := e.sql(); len(got) != 1 || !strings.HasPrefix(got[0], "REVOKE") {
		t.Errorf("SQL after a failed revoke: %q", got)
	}
	if e.stateFile(t, "blog") != before {
		t.Error("state changed")
	}

	// MySQL down is a conflict, and nothing changes.
	e2 := newDBEnv(t)
	e2.saveSite(t, st)
	e2.answer("", errDown)
	_, err := e2.job(t, "db.drop", drop("blog", "s_blog_a"))
	wantCode(t, err, contract.ErrConflict)
	if e2.stateFile(t, "blog") != before {
		t.Error("state changed while MySQL was down")
	}
}

func TestListReportsSizes(t *testing.T) {
	e := newDBEnv(t)
	for name, dbs := range map[contract.SiteName][]contract.DatabaseName{
		"blog":   {"s_blog_a", "s_blog_gone"},
		"shop-x": {"s_shop_x_main"},
	} {
		st := e.site(t, name)
		st.Databases = dbs
		e.saveSite(t, st)
	}
	// information_schema and an untracked database are in MySQL's answer
	// and not in the result.
	rows := "information_schema\t0\ns_blog_a\t16384\ns_shop_x_main\t0\ns_untracked_x\t99\n"

	e.answer(rows, "")
	res, err := e.list(t, "")
	if err != nil {
		t.Fatal(err)
	}
	want := contract.DbListResult{Databases: []contract.DatabaseInfo{
		{Name: "s_blog_a", Site: "blog", SizeBytes: 16384},
		{Name: "s_blog_gone", Site: "blog", SizeBytes: 0}, // tracked, missing in MySQL
		{Name: "s_shop_x_main", Site: "shop-x", SizeBytes: 0},
	}}
	if !jsonEqual(t, res, want) {
		t.Errorf("list = %+v\nwant   %+v", res, want)
	}

	e.answer(rows, "")
	res, err = e.list(t, "shop-x")
	if err != nil {
		t.Fatal(err)
	}
	want = contract.DbListResult{Databases: []contract.DatabaseInfo{{Name: "s_shop_x_main", Site: "shop-x", SizeBytes: 0}}}
	if !jsonEqual(t, res, want) {
		t.Errorf("filtered list = %+v\nwant   %+v", res, want)
	}
	if sql := e.sql()[0]; !strings.HasPrefix(sql, "SET SESSION information_schema_stats_expiry = 0;") {
		t.Errorf("sizes query = %q", sql)
	}
}

func TestListWithoutDatabasesEncodesAnEmptyArray(t *testing.T) {
	e := newDBEnv(t)
	e.answer("information_schema\t0\n", "")
	res, err := e.list(t, "")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(res)
	if string(b) != `{"databases":[]}` {
		t.Errorf("encoded %s", b)
	}
}

func TestListWhenMySQLIsMissingOrDown(t *testing.T) {
	e := newDBEnv(t)
	e.answer("", errDown)
	_, err := e.list(t, "")
	wantCode(t, err, contract.ErrConflict)

	if err := os.Remove(e.p.ServicesState()); err != nil {
		t.Fatal(err)
	}
	e.run = sysexec.NewFakeRunner()
	e.d.Run = e.run
	res, err := e.list(t, "")
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := json.Marshal(res); string(b) != `{"databases":[]}` || len(e.run.Calls()) != 0 {
		t.Errorf("not installed: %s, %d runs", b, len(e.run.Calls()))
	}

	_, err = e.list(t, "nope")
	wantCode(t, err, contract.ErrNotFound)
}

func jsonEqual(t *testing.T, a, b any) bool {
	t.Helper()
	x, err1 := json.Marshal(a)
	y, err2 := json.Marshal(b)
	if err1 != nil || err2 != nil {
		t.Fatal(err1, err2)
	}
	return string(x) == string(y)
}

// shop-x owns the names under its longer prefix, even while shop exists.
func TestCreateByTheLongerSiteSucceeds(t *testing.T) {
	e := newDBEnv(t)
	e.answer("", "")
	if _, err := e.job(t, "db.create", create("shop-x", "s_shop_x_main")); err != nil {
		t.Fatal(err)
	}
	if got := e.site(t, "shop-x").Databases; len(got) != 1 || got[0] != "s_shop_x_main" {
		t.Errorf("databases %v", got)
	}
	// And shop may not take a name of shop-x's.
	_, err := e.job(t, "db.create", create("shop", "s_shop_x_other"))
	if ae := wantCode(t, err, contract.ErrConflict); !strings.Contains(ae.Message, "belongs to site shop-x") {
		t.Errorf("message %q", ae.Message)
	}
}

const errExists = "ERROR 1007 (HY000) at line 1: Can't create database 's_blog_a'; database exists"

// An empty database that exists in MySQL and that no site lists is taken
// over: the grant and the state follow as after a fresh create.
func TestCreateTakesOverAnEmptyExistingDatabase(t *testing.T) {
	e := newDBEnv(t)
	st := e.site(t, "blog")
	st.DBUser = true
	e.saveSite(t, st)
	e.answer("", errExists)
	e.answer("0\n", "")
	e.answer("", "")
	res, err := e.job(t, "db.create", create("blog", "s_blog_a"))
	if err != nil {
		t.Fatal(err)
	}
	if res != (contract.DatabaseResult{Site: "blog", Database: "s_blog_a"}) {
		t.Errorf("result %+v", res)
	}
	got := e.sql()
	if len(got) != 3 || !strings.HasPrefix(got[0], "CREATE DATABASE `s_blog_a`") ||
		!strings.Contains(got[1], "information_schema.tables WHERE table_schema = 's_blog_a'") || !strings.HasPrefix(got[2], "GRANT ALL PRIVILEGES ON ") {
		t.Fatalf("SQL = %q", got)
	}
	if after := e.site(t, "blog"); len(after.Databases) != 1 || after.Databases[0] != "s_blog_a" {
		t.Errorf("state %+v", after)
	}
}

func TestCreateRefusesAnExistingDatabaseWithContent(t *testing.T) {
	e := newDBEnv(t)
	before := e.stateFile(t, "blog")
	e.answer("", errExists)
	e.answer("2\n", "")
	_, err := e.job(t, "db.create", create("blog", "s_blog_a"))
	ae := wantCode(t, err, contract.ErrConflict)
	if !strings.Contains(ae.Message, "with content") {
		t.Errorf("message %q", ae.Message)
	}
	if e.stateFile(t, "blog") != before || len(e.sql()) != 2 {
		t.Errorf("state changed or more SQL ran: %q", e.sql())
	}
}

// When the cleanup after a failure fails too, the recorded message says the
// empty database stays and a new create takes it over. The job records an
// AgentError's Message only, so the text must be in it.
func TestCreateCleanupFailureIsInTheRecordedMessage(t *testing.T) {
	e := newDBEnv(t)
	st := e.site(t, "blog")
	st.DBUser = true
	e.saveSite(t, st)
	e.answer("", "")
	e.answer("", "ERROR 2013 (HY000): Lost connection to MySQL server")
	e.answer("", "ERROR 2013 (HY000): Lost connection to MySQL server")
	_, err := e.job(t, "db.create", create("blog", "s_blog_a"))
	ae := wantCode(t, err, contract.ErrConflict)
	for _, want := range []string{"isn't running", "s_blog_a was left in MySQL", "take it over"} {
		if !strings.Contains(ae.Message, want) {
			t.Errorf("message %q lacks %q", ae.Message, want)
		}
	}
}

// A create whose job was cancelled may or may not have made the database.
// Nothing is dropped blindly; the next create takes the database over.
func TestCreateAfterACancelledCreateTakesTheDatabaseOver(t *testing.T) {
	e := newDBEnv(t)
	before := e.stateFile(t, "blog")
	e.run.Add(sysexec.Script{Path: mysqlClient, Args: mysqlClientArgs, Err: context.Canceled})
	_, err := e.job(t, "db.create", create("blog", "s_blog_a"))
	if err == nil {
		t.Fatal("no error")
	}
	if got := e.sql(); len(got) != 1 {
		t.Errorf("SQL after the cancelled create: %q", got)
	}
	if e.stateFile(t, "blog") != before {
		t.Error("state changed")
	}
	e.answer("", errExists)
	e.answer("0\n", "")
	if _, err := e.job(t, "db.create", create("blog", "s_blog_a")); err != nil {
		t.Fatal(err)
	}
	if got := e.site(t, "blog").Databases; len(got) != 1 || got[0] != "s_blog_a" {
		t.Errorf("databases %v", got)
	}
}
