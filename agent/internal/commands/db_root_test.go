//go:build roottest

package commands_test

import (
	"context"
	"errors"
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

// This test installs MySQL on the machine it runs on and writes site state
// under /var/lib/zimam, so it runs only where that is wanted: a throwaway
// container, with ZIMAM_ROOTTEST_MYSQL=1, as go test -tags roottest.

func TestDbCommandsAgainstRealMySQL(t *testing.T) {
	if os.Getenv("ZIMAM_ROOTTEST_MYSQL") != "1" {
		t.Skip("set ZIMAM_ROOTTEST_MYSQL=1 to install MySQL on this machine")
	}
	if os.Geteuid() != 0 {
		t.Fatal("the roottest tests need root")
	}
	ctx := context.Background()
	p := paths.Paths{}
	d := sysexec.Deps{Paths: p, Run: sysexec.ExecRunner{}, Logf: t.Logf, Now: time.Now}
	if _, err := mysql.Install(ctx, d); err != nil {
		t.Fatalf("Install: %v", err)
	}
	dbs := []contract.DatabaseName{"s_aa_adopt", "s_aa_full", "s_aa_gone"}
	cleanup := func() {
		for _, db := range dbs {
			mysql.Exec(ctx, d, mysql.RevokeAll("aa", db), mysql.DropDatabase(db))
		}
		mysql.Exec(ctx, d, mysql.DropUser("aa"))
		os.Remove(p.SiteState("aa"))
	}
	cleanup()
	t.Cleanup(cleanup)
	site := sites.State{Name: "aa", Domain: "aa.example.com", Type: contract.SiteStatic, TemplateVersion: 1, Security: sites.DefaultSecurity(), DBUser: true}
	if err := sites.Save(p, site); err != nil {
		t.Fatal(err)
	}
	pw := contract.ServicePassword("Aaaaaaaaaaaaaaaaaaaaaaaa-1_aaaa")
	if _, err := mysql.Exec(ctx, d, mysql.CreateUser("aa", pw), mysql.AlterUserPassword("aa", pw)); err != nil {
		t.Fatal(err)
	}
	j := &jobs.Context{ID: "01JBX7Q3A1B2C3D4E5F6G7H8J9", Logf: t.Logf, Paths: p, Run: sysexec.ExecRunner{}}
	run := func(command string, params contract.Params) error {
		_, err := commands.JobHandlers()[command](ctx, j, params)
		return err
	}
	count := func(db contract.DatabaseName) string {
		rows, err := mysql.Exec(ctx, d, mysql.DatabaseObjects(db))
		if err != nil || len(rows) != 1 {
			t.Fatalf("count %s: %v, %v", db, rows, err)
		}
		return rows[0][0]
	}
	exists := func(db contract.DatabaseName) bool {
		t.Helper()
		sizes, err := mysql.Sizes(ctx, d)
		if err != nil {
			t.Fatal(err)
		}
		_, ok := sizes[string(db)]
		return ok
	}
	listed := func() []contract.DatabaseName {
		st, err := sites.Load(p, "aa")
		if err != nil {
			t.Fatal(err)
		}
		return st.Databases
	}

	// An empty database made by hand is taken over: granted and listed.
	if _, err := mysql.Exec(ctx, d, mysql.CreateDatabase("s_aa_adopt")); err != nil {
		t.Fatal(err)
	}
	if err := run("db.create", &contract.DbCreateParams{Site: "aa", Database: "s_aa_adopt"}); err != nil {
		t.Fatalf("db.create of an empty database that exists: %v", err)
	}
	if got := listed(); len(got) != 1 || got[0] != "s_aa_adopt" {
		t.Errorf("listed after the takeover: %v", got)
	}

	// One that holds a table is refused and left alone.
	if _, err := mysql.Exec(ctx, d, mysql.CreateDatabase("s_aa_full")); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("/usr/bin/mysql", "--protocol=socket", "--user=root", "--execute=CREATE TABLE s_aa_full.t (id INT)").CombinedOutput(); err != nil {
		t.Fatalf("%s: %v", out, err)
	}
	err := run("db.create", &contract.DbCreateParams{Site: "aa", Database: "s_aa_full"})
	var ae *contract.AgentError
	if !errors.As(err, &ae) || ae.Code != contract.ErrConflict || !strings.Contains(ae.Message, "with content") {
		t.Errorf("db.create of a database with a table: %v", err)
	}
	if got := listed(); len(got) != 1 {
		t.Errorf("listed after the refusal: %v", got)
	}
	if n := count("s_aa_full"); n != "1" {
		t.Errorf("the refused database holds %s objects, want its 1 table", n)
	}

	// A tracked database removed by hand is still dropped from the state.
	if err := run("db.create", &contract.DbCreateParams{Site: "aa", Database: "s_aa_gone"}); err != nil {
		t.Fatal(err)
	}
	if _, err := mysql.Exec(ctx, d, mysql.DropDatabase("s_aa_gone")); err != nil {
		t.Fatal(err)
	}
	if exists("s_aa_gone") {
		t.Fatal("s_aa_gone is still in MySQL")
	}
	if err := run("db.drop", &contract.DbDropParams{Site: "aa", Database: "s_aa_gone"}); err != nil {
		t.Fatalf("db.drop of a database that is already gone: %v", err)
	}
	if got := listed(); len(got) != 1 || got[0] != "s_aa_adopt" {
		t.Errorf("listed after the drop: %v", got)
	}

	// And the taken-over database drops like any other.
	if err := run("db.drop", &contract.DbDropParams{Site: "aa", Database: "s_aa_adopt"}); err != nil {
		t.Fatal(err)
	}
	if exists("s_aa_adopt") || len(listed()) != 0 {
		t.Error("s_aa_adopt is still there after db.drop")
	}
}
