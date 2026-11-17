package commands_test

import (
	"context"
	"os"
	"strings"
	"testing"

	"zimam/agent/contract"
	"zimam/agent/internal/commands"
	"zimam/agent/internal/jobs"
	"zimam/agent/internal/sysexec"
)

const lostConnection = "ERROR 2013 (HY000): Lost connection to MySQL server"

// When the cleanup after a failed create fails too, the database stays in
// MySQL with the site's grant on it. The site's state lists it anyway, so
// db.list shows it, db.user accepts the grant and db.drop removes it; the
// error says it was kept.
func TestCreateWhoseCleanupFailsKeepsTheDatabaseInTheState(t *testing.T) {
	e := newDBEnv(t)
	st := e.site(t, "blog")
	st.DBUser = true
	e.saveSite(t, st)
	e.answer("", "")
	e.answer("", "")
	e.answer("", lostConnection) // the grant
	e.answer("", lostConnection) // the revoke
	_, err := e.job(t, "db.create", create("blog", "s_blog_a"))
	ae := wantCode(t, err, contract.ErrConflict)
	for _, want := range []string{"s_blog_a was kept", "db.drop"} {
		if !strings.Contains(ae.Message, want) {
			t.Errorf("message %q lacks %q", ae.Message, want)
		}
	}
	if got := e.site(t, "blog").Databases; len(got) != 1 || got[0] != "s_blog_a" {
		t.Errorf("state lists %v, want s_blog_a", got)
	}
	// It is now the site's: a second create is refused as a duplicate.
	wantCode(t, e.precheck(t, "db.create", create("blog", "s_blog_a")), contract.ErrConflict)
}

// The same for a database that existed before: its grant may remain, and the
// database is the site's from now on.
func TestCreateWhoseTakeoverCleanupFailsKeepsTheDatabaseInTheState(t *testing.T) {
	e := takeoverEnv(t, "")
	e.answer("", lostConnection)
	e.answer("", lostConnection)
	_, err := e.job(t, "db.create", create("blog", "s_blog_a"))
	ae := wantCode(t, err, contract.ErrConflict)
	if !strings.Contains(ae.Message, "s_blog_a was kept") || !strings.Contains(ae.Message, "db.drop") {
		t.Errorf("message %q", ae.Message)
	}
	if got := e.site(t, "blog").Databases; len(got) != 1 || got[0] != "s_blog_a" {
		t.Errorf("state lists %v, want s_blog_a", got)
	}
}

// When saving the state is what failed and the cleanup fails as well, the
// database stays unlisted; the error says plainly that it was left, and a
// second create adopts it.
func TestCreateWhoseStateAndCleanupFailSaysTheDatabaseWasLeft(t *testing.T) {
	e := newDBEnv(t)
	st := e.site(t, "blog")
	st.DBUser = true
	e.saveSite(t, st)
	e.run = sysexec.NewFakeRunner()
	hook := &hookRunner{Runner: e.run, after: 3, do: breakState(t, e, "blog")}
	for range 3 {
		e.run.Add(sysexec.Script{Path: mysqlClient, Args: mysqlClientArgs})
	}
	e.run.Add(sysexec.Script{Path: mysqlClient, Args: mysqlClientArgs, Result: sysexec.Result{ExitCode: 1, Stderr: []byte(lostConnection)}})
	j := &jobs.Context{ID: "01JBX7Q3A1B2C3D4E5F6G7H8J9", Logf: t.Logf, Paths: e.p, Run: hook}
	_, err := commands.JobHandlers()["db.create"](context.Background(), j, create("blog", "s_blog_a"))
	if err == nil {
		t.Fatal("no error")
	}
	for _, want := range []string{"The database s_blog_a was left in MySQL", "not in the site's state", "db.create for s_blog_a again"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q lacks %q", err, want)
		}
	}
	// Give the state back: the next create takes the database over, with its
	// own grant cleared first, then lists it.
	if err := os.Remove(e.p.SiteState("blog")); err != nil {
		t.Fatal(err)
	}
	st.Databases = nil
	e.saveSite(t, st)
	e.run = sysexec.NewFakeRunner()
	e.d.Run = e.run
	e.answer("", errExists)
	e.answer("0\n", "")
	e.answer(batchRow("s_blog", "localhost", `s\_blog\_a`)+batchRow("s_blog", "127.0.0.1", `s\_blog\_a`), "")
	e.answer("", "") // the revoke
	e.answer("", "")
	e.answer("", "")
	if _, err := e.job(t, "db.create", create("blog", "s_blog_a")); err != nil {
		t.Fatal(err)
	}
	if got := e.site(t, "blog").Databases; len(got) != 1 || got[0] != "s_blog_a" {
		t.Errorf("state lists %v after the second create", got)
	}
}

// A grant of Zimam's own form on a database the site doesn't list is what an
// unfinished create leaves; db.user says so and points to db.create.
func TestDbUserNamesAnUnlistedDatabaseWithTheSitesGrant(t *testing.T) {
	e := shopWithUser(t)
	e.answer("", "")
	e.answer(privRow("database", "s_shop", "localhost", `s\_shop\_a`), "")
	_, err := e.job(t, "db.user", userParams("shop", pwOne))
	ae := wantCode(t, err, contract.ErrConflict)
	for _, want := range []string{"s_shop_a", "db.create"} {
		if !strings.Contains(ae.Message, want) {
			t.Errorf("message %q lacks %q", ae.Message, want)
		}
	}
	if strings.Contains(ae.Message, "did not give") {
		t.Errorf("message %q says Zimam didn't give the grant", ae.Message)
	}
	// A grant that is not Zimam's form stays a foreign privilege.
	e = shopWithUser(t)
	e.answer("", "")
	e.answer(privRow("database", "s_shop", "localhost", "s_shop_a"), "")
	_, err = e.job(t, "db.user", userParams("shop", pwOne))
	if ae := wantCode(t, err, contract.ErrConflict); !strings.Contains(ae.Message, "did not give") {
		t.Errorf("message %q", ae.Message)
	}
}
