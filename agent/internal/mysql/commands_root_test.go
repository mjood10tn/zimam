//go:build roottest

package mysql_test

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

// asMySQLUser runs one statement as a MySQL user over the socket and returns
// what the client printed, errors included.
func asMySQLUser(t *testing.T, user string, pw contract.ServicePassword, sql string) (string, error) {
	t.Helper()
	cmd := exec.Command("/usr/bin/mysql", "--protocol=socket", "--user="+user, "--batch", "--skip-column-names", "--execute="+sql)
	cmd.Env = append(os.Environ(), "MYSQL_PWD="+string(pw))
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func rootSQL(t *testing.T, sql string) {
	t.Helper()
	if out, err := exec.Command("/usr/bin/mysql", "--protocol=socket", "--user=root", "--execute="+sql).CombinedOutput(); err != nil {
		t.Fatalf("%s: %v\n%s", sql, err, out)
	}
}

// A takeover or a create must not leave another account with a grant on the
// database, so that sites stay apart.
func TestDbCreateClearsOrRefusesOtherGrantsAgainstRealMySQL(t *testing.T) {
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
	cleanup := func() {
		for _, db := range []string{"s_shop_x_a", "s_shop_x_c", "s_shop_x_d", "s_shop_x_pat"} {
			exec.Command("/usr/bin/mysql", "--protocol=socket", "--user=root", "--execute=DROP DATABASE IF EXISTS "+db).Run()
		}
		for _, u := range []string{"s_shop'@'localhost", "s_shop'@'127.0.0.1", "s_shop-x'@'localhost", "s_shop-x'@'127.0.0.1", "hand'@'localhost", "hand2'@'localhost"} {
			exec.Command("/usr/bin/mysql", "--protocol=socket", "--user=root", "--execute=DROP USER IF EXISTS '"+u+"'").Run()
		}
		os.Remove(p.SiteState("shop"))
		os.Remove(p.SiteState("shop-x"))
	}
	cleanup()
	t.Cleanup(cleanup)

	pwShop, pwShopX := contract.ServicePassword("Sssssssssssssssssssssssss-1_ssss"), contract.ServicePassword("Xxxxxxxxxxxxxxxxxxxxxxxx-2_xxxx")
	for _, st := range []sites.State{
		{Name: "shop", Domain: "shop.example.com", Type: contract.SiteStatic, TemplateVersion: 1, Security: sites.DefaultSecurity(), DBUser: true},
		{Name: "shop-x", Domain: "shopx.example.com", Type: contract.SiteStatic, TemplateVersion: 1, Security: sites.DefaultSecurity(), DBUser: true},
	} {
		if err := sites.Save(p, st); err != nil {
			t.Fatal(err)
		}
	}
	for site, pw := range map[contract.SiteName]contract.ServicePassword{"shop": pwShop, "shop-x": pwShopX} {
		if _, err := mysql.Exec(ctx, d, mysql.CreateUser(site, pw), mysql.AlterUserPassword(site, pw)); err != nil {
			t.Fatal(err)
		}
	}
	j := &jobs.Context{ID: "01JBX7Q3A1B2C3D4E5F6G7H8J9", Logf: t.Logf, Paths: p, Run: sysexec.ExecRunner{}}
	run := func(site contract.SiteName, db contract.DatabaseName) error {
		_, err := commands.JobHandlers()["db.create"](ctx, j, &contract.DbCreateParams{Site: site, Database: db})
		return err
	}
	exists := func(db contract.DatabaseName) bool {
		sizes, err := mysql.Sizes(ctx, d)
		if err != nil {
			t.Fatal(err)
		}
		_, ok := sizes[string(db)]
		return ok
	}

	// (b) shop's user was left with grants on databases that shop-x now
	// creates: one written as Zimam writes it, one with the name as it is.
	rootSQL(t, "CREATE DATABASE s_shop_x_a")
	if _, err := mysql.Exec(ctx, d, mysql.GrantAll("shop", "s_shop_x_a")); err != nil {
		t.Fatal(err)
	}
	rootSQL(t, "CREATE DATABASE s_shop_x_c")
	rootSQL(t, "GRANT ALL ON `s_shop_x_c`.* TO 's_shop'@'localhost', 's_shop'@'127.0.0.1'")
	for _, db := range []contract.DatabaseName{"s_shop_x_a", "s_shop_x_c"} {
		before, _ := mysql.Grantees(ctx, d, db)
		t.Logf("grantees of %s before the takeover: %v", db, before)
		if out, err := asMySQLUser(t, "s_shop", pwShop, "SHOW TABLES FROM "+string(db)); err != nil {
			t.Fatalf("setup: shop's user should reach %s: %s", db, out)
		}
		if err := run("shop-x", db); err != nil {
			t.Fatalf("db.create %s by shop-x: %v", db, err)
		}
		after, err := mysql.Grantees(ctx, d, db)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("grantees of %s after the takeover: %v", db, after)
		if len(after) != 2 || after[0] != (mysql.Grantee{User: "s_shop-x", Host: "127.0.0.1", Pattern: strings.ReplaceAll(string(db), "_", `\_`)}) ||
			after[1] != (mysql.Grantee{User: "s_shop-x", Host: "localhost", Pattern: strings.ReplaceAll(string(db), "_", `\_`)}) {
			t.Errorf("grantees of %s after the takeover: %v, want only shop-x's two accounts", db, after)
		}
		if out, err := asMySQLUser(t, "s_shop", pwShop, "SHOW TABLES FROM "+string(db)); err == nil || !strings.Contains(out, "ERROR 1044") {
			t.Errorf("shop's user still reaches %s: %q, %v", db, out, err)
		}
		if out, err := asMySQLUser(t, "s_shop-x", pwShopX, "CREATE TABLE "+string(db)+".t (id INT)"); err != nil {
			t.Errorf("shop-x's user can't use %s: %q, %v", db, out, err)
		}
	}

	// (a) a user someone made by hand holds a grant: the database is refused
	// and left as it was, and so is its grant.
	rootSQL(t, "CREATE DATABASE s_shop_x_d")
	rootSQL(t, "CREATE USER 'hand'@'localhost'")
	rootSQL(t, "GRANT ALL ON `s"+`\_shop\_x\_d`+"`.* TO 'hand'@'localhost'")
	err := run("shop-x", "s_shop_x_d")
	var ae *contract.AgentError
	if !errors.As(err, &ae) || ae.Code != contract.ErrConflict || !strings.Contains(ae.Message, "'hand'@'localhost'") {
		t.Errorf("db.create over a hand-made grant: %v", err)
	}
	if g, _ := mysql.Grantees(ctx, d, "s_shop_x_d"); len(g) != 1 || g[0].User != "hand" {
		t.Errorf("grantees of s_shop_x_d after the refusal: %v", g)
	}

	// A wildcard grant that covers a new name refuses it, and the database
	// that was just created is dropped again.
	rootSQL(t, "CREATE USER 'hand2'@'localhost'")
	rootSQL(t, "GRANT ALL ON `s"+`\_shop\_x\_pat`+"%`.* TO 'hand2'@'localhost'")
	err = run("shop-x", "s_shop_x_pat")
	if !errors.As(err, &ae) || ae.Code != contract.ErrConflict || !strings.Contains(ae.Message, "'hand2'@'localhost'") {
		t.Errorf("db.create under a wildcard grant: %v", err)
	}
	if exists("s_shop_x_pat") {
		t.Error("the database refused for a wildcard grant was left in MySQL")
	}
	st, err := sites.Load(p, "shop-x")
	if err != nil || len(st.Databases) != 2 {
		t.Errorf("shop-x lists %v, %v; want only the two taken over", st.Databases, err)
	}
}
