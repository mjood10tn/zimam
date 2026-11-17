//go:build roottest

package mysql

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"

	"zimam/agent/contract"
)

// This test creates users and databases in the MySQL of the machine it
// runs on (installing MySQL first if needed), so it runs only where that is
// wanted: a throwaway container, with ZIMAM_ROOTTEST_MYSQL=1, as
// go test -tags roottest ./internal/mysql.

const (
	pwOne contract.ServicePassword = "Aaaaaaaaaaaaaaaaaaaaaaaa-1_aaaa"
	pwTwo contract.ServicePassword = "Bbbbbbbbbbbbbbbbbbbbbbbb-2_bbbb"
)

// asSiteUser runs one statement as the site's user with its password, over
// the socket or, with tcp, over 127.0.0.1, and returns what the client
// printed, errors included.
func asSiteUser(t *testing.T, site contract.SiteName, pw contract.ServicePassword, tcp bool, sql string) (string, error) {
	t.Helper()
	args := []string{"--protocol=socket"}
	if tcp {
		args = []string{"--protocol=tcp", "--host=127.0.0.1"}
	}
	args = append(args, "--user=s_"+string(site), "--batch", "--skip-column-names", "--execute="+sql)
	cmd := exec.Command(clientPath, args...)
	cmd.Env = append(os.Environ(), "MYSQL_PWD="+string(pw))
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func TestCreateAndDropAgainstRealMySQL(t *testing.T) {
	d := rootDeps(t)
	ctx := context.Background()
	if _, err := Install(ctx, d); err != nil {
		t.Fatalf("Install: %v", err)
	}
	must := func(statements ...Statement) [][]string {
		t.Helper()
		rows, err := Exec(ctx, d, statements...)
		if err != nil {
			t.Fatal(err)
		}
		return rows
	}
	cleanup := func() {
		for _, db := range []contract.DatabaseName{"s_aa_x", "s_aaxx", "s_aa_e", "s_bb_x"} {
			Exec(ctx, d, RevokeAll("aa", db), DropDatabase(db))
		}
		Exec(ctx, d, DropUser("aa"), DropUser("bb"))
	}
	cleanup()
	t.Cleanup(cleanup)

	// A database gets the character set and collation, and a second create
	// of the same name is MySQL's error 1007, never an adoption.
	must(CreateDatabase("s_aa_x"))
	if got := mustQuery(t, d, "SELECT DEFAULT_CHARACTER_SET_NAME, DEFAULT_COLLATION_NAME FROM information_schema.schemata WHERE schema_name = 's_aa_x';"); got != "utf8mb4\tutf8mb4_0900_ai_ci" {
		t.Errorf("character set and collation = %q", got)
	}
	var me *Error
	if _, err := Exec(ctx, d, CreateDatabase("s_aa_x")); !errors.As(err, &me) || me.Errno() != ErrDatabaseExists {
		t.Errorf("second create: %v, want error %d", err, ErrDatabaseExists)
	}

	// The user's grant on s_aa_x must not reach s_aaxx, which the pattern
	// s_aa_x would match if its underscores weren't escaped.
	must(CreateDatabase("s_aaxx"), CreateDatabase("s_aa_e"))
	mustQuery(t, d, "CREATE TABLE s_aa_x.t (id INT); INSERT INTO s_aa_x.t VALUES (1);")
	mustQuery(t, d, "CREATE TABLE s_aaxx.t (id INT); INSERT INTO s_aaxx.t VALUES (42);")
	must(CreateUser("aa"), AlterUserPassword("aa", pwOne), GrantAll("aa", "s_aa_x"))
	// Running the user statements again changes nothing that matters.
	must(CreateUser("aa"), AlterUserPassword("aa", pwOne), GrantAll("aa", "s_aa_x"))
	for _, tcp := range []bool{false, true} {
		if out, err := asSiteUser(t, "aa", pwOne, tcp, "SELECT id FROM s_aa_x.t"); err != nil || out != "1" {
			t.Errorf("tcp=%v: the user's own database: %q, %v", tcp, out, err)
		}
		out, err := asSiteUser(t, "aa", pwOne, tcp, "SELECT id FROM s_aaxx.t")
		if err == nil || !strings.Contains(out, "ERROR 1142") {
			t.Errorf("tcp=%v: the user read s_aaxx: %q, %v", tcp, out, err)
		}
	}
	if out, _ := asSiteUser(t, "aa", pwOne, false, "SELECT CURRENT_USER()"); out != "s_aa@localhost" {
		t.Errorf("socket login is %q", out)
	}
	if out, _ := asSiteUser(t, "aa", pwOne, true, "SELECT CURRENT_USER()"); out != "s_aa@127.0.0.1" {
		t.Errorf("tcp login is %q", out)
	}

	// A new password replaces the old one on both accounts.
	must(AlterUserPassword("aa", pwTwo))
	for _, tcp := range []bool{false, true} {
		if _, err := asSiteUser(t, "aa", pwOne, tcp, "SELECT 1"); err == nil {
			t.Errorf("tcp=%v: the old password still works", tcp)
		}
		if out, err := asSiteUser(t, "aa", pwTwo, tcp, "SELECT 1"); err != nil || out != "1" {
			t.Errorf("tcp=%v: the new password: %q, %v", tcp, out, err)
		}
	}

	// Sizes: a database with a table is above 0, an empty one is 0, and
	// the numbers are current without waiting for MySQL's cache.
	sizes, err := Sizes(ctx, d)
	if err != nil {
		t.Fatal(err)
	}
	if sizes["s_aa_x"] <= 0 || sizes["s_aa_e"] != 0 {
		t.Errorf("sizes: s_aa_x = %d, s_aa_e = %d", sizes["s_aa_x"], sizes["s_aa_e"])
	}
	if _, ok := sizes["s_aa_e"]; !ok {
		t.Error("the empty database is missing from the sizes")
	}
	before := sizes["s_aa_x"]
	mustQuery(t, d, "INSERT INTO s_aa_x.t SELECT a.id FROM s_aa_x.t a, information_schema.columns b, information_schema.columns c LIMIT 200000;")
	// InnoDB refreshes its own statistics in the background a moment after
	// a table changes a lot, so the size may take a few seconds to follow.
	grown := false
	for range 30 {
		if sizes, err = Sizes(ctx, d); err != nil {
			t.Fatal(err)
		}
		if grown = sizes["s_aa_x"] > before; grown {
			break
		}
		time.Sleep(time.Second)
	}
	t.Logf("size of s_aa_x: %d before the inserts, %d after", before, sizes["s_aa_x"])
	if !grown {
		t.Errorf("size after inserting rows: %d; was %d", sizes["s_aa_x"], before)
	}

	// Dropping revokes first: afterwards the grant is gone, so the user
	// can't create the database again.
	must(RevokeAll("aa", "s_aa_x"), DropDatabase("s_aa_x"))
	if got := mustQuery(t, d, "SELECT COUNT(*) FROM information_schema.schemata WHERE schema_name = 's_aa_x';"); got != "0" {
		t.Errorf("s_aa_x exists after the drop: %s", got)
	}
	for _, host := range []string{"localhost", "127.0.0.1"} {
		if got := mustQuery(t, d, "SHOW GRANTS FOR 's_aa'@'"+host+"';"); strings.Contains(got, "ALL PRIVILEGES") {
			t.Errorf("grants of s_aa@%s after the drop:\n%s", host, got)
		}
	}
	for _, tcp := range []bool{false, true} {
		out, err := asSiteUser(t, "aa", pwTwo, tcp, "CREATE DATABASE s_aa_x")
		if err == nil || !strings.Contains(out, "ERROR 1044") {
			t.Errorf("tcp=%v: the user created the dropped database again: %q, %v", tcp, out, err)
		}
	}
	// Dropping again, or revoking for a site that has no user, succeeds.
	must(RevokeAll("aa", "s_aa_x"), DropDatabase("s_aa_x"))
	must(RevokeAll("bb", "s_bb_x"), DropDatabase("s_bb_x"))

	// The user's removal takes both accounts, and is repeatable.
	must(DropUser("aa"))
	must(DropUser("aa"))
	if got := mustQuery(t, d, "SELECT COUNT(*) FROM mysql.user WHERE user = 's_aa';"); got != "0" {
		t.Errorf("accounts left after DropUser: %s", got)
	}
}

// A name whose escaped form is exactly 64 characters, the longest the
// params allow, takes a grant and a revoke. MySQL refuses a grant name of 65
// characters or more (error 1059), which is why the params limit a name's
// length plus its underscores.
func TestLongDatabaseNamesAgainstRealMySQL(t *testing.T) {
	d := rootDeps(t)
	ctx := context.Background()
	if _, err := Install(ctx, d); err != nil {
		t.Fatalf("Install: %v", err)
	}
	manyUnderscores := contract.DatabaseName("s_aa_" + strings.Repeat("a_", 18) + "aaa")
	fewUnderscores := contract.DatabaseName("s_aa_" + strings.Repeat("a", 57))
	for _, db := range []contract.DatabaseName{manyUnderscores, fewUnderscores} {
		if g := len(db) + strings.Count(string(db), "_"); g != 64 || db.Validate() != nil {
			t.Fatalf("%s: %d characters when escaped, %v", db, g, db.Validate())
		}
	}
	cleanup := func() {
		for _, db := range []contract.DatabaseName{manyUnderscores, fewUnderscores} {
			Exec(ctx, d, RevokeAll("aa", db), DropDatabase(db))
		}
		Exec(ctx, d, DropUser("aa"))
	}
	cleanup()
	t.Cleanup(cleanup)
	if _, err := Exec(ctx, d, CreateUser("aa"), AlterUserPassword("aa", pwOne)); err != nil {
		t.Fatal(err)
	}
	for _, db := range []contract.DatabaseName{manyUnderscores, fewUnderscores} {
		t.Logf("%s: %d characters, %d underscores, %d in the grant", db, len(db), strings.Count(string(db), "_"), len(db)+strings.Count(string(db), "_"))
		_, err := Exec(ctx, d, CreateDatabase(db), GrantAll("aa", db))
		t.Logf("create and grant: %v", err)
		if err != nil {
			t.Errorf("%s: %v", db, err)
			continue
		}
		mustQuery(t, d, "CREATE TABLE `"+string(db)+"`.t (id INT); INSERT INTO `"+string(db)+"`.t VALUES (7);")
		if out, err := asSiteUser(t, "aa", pwOne, false, "SELECT id FROM `"+string(db)+"`.t"); err != nil || out != "7" {
			t.Errorf("%s: the user can't use its database: %q, %v", db, out, err)
		}
		if _, err := Exec(ctx, d, RevokeAll("aa", db), DropDatabase(db)); err != nil {
			t.Errorf("%s: revoke and drop: %v", db, err)
		}
		if got := mustQuery(t, d, "SHOW GRANTS FOR 's_aa'@'localhost';"); strings.Contains(got, "ALL PRIVILEGES") {
			t.Errorf("%s: grants left after the revoke:\n%s", db, got)
		}
	}
}

func TestObjectCountAgainstRealMySQL(t *testing.T) {
	d := rootDeps(t)
	ctx := context.Background()
	if _, err := Install(ctx, d); err != nil {
		t.Fatalf("Install: %v", err)
	}
	cleanup := func() { Exec(ctx, d, DropDatabase("s_aa_cnt")) }
	cleanup()
	t.Cleanup(cleanup)
	count := func() int64 {
		t.Helper()
		n, err := ObjectCount(ctx, d, "s_aa_cnt")
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := count(); n != 0 {
		t.Errorf("a database that doesn't exist: %d", n)
	}
	if _, err := Exec(ctx, d, CreateDatabase("s_aa_cnt")); err != nil {
		t.Fatal(err)
	}
	if n := count(); n != 0 {
		t.Errorf("an empty database: %d", n)
	}
	mustQuery(t, d, "CREATE TABLE s_aa_cnt.t (id INT);")
	if n := count(); n != 1 {
		t.Errorf("one table: %d", n)
	}
	mustQuery(t, d, "DROP TABLE s_aa_cnt.t; CREATE VIEW s_aa_cnt.v AS SELECT 1 AS one;")
	if n := count(); n != 1 {
		t.Errorf("one view: %d", n)
	}
	mustQuery(t, d, "DROP VIEW s_aa_cnt.v; CREATE FUNCTION s_aa_cnt.f() RETURNS INT DETERMINISTIC RETURN 1;")
	if n := count(); n != 1 {
		t.Errorf("one routine: %d", n)
	}
	mustQuery(t, d, "DROP FUNCTION s_aa_cnt.f; CREATE EVENT s_aa_cnt.ev ON SCHEDULE EVERY 1 DAY DO SELECT 1;")
	if n := count(); n != 1 {
		t.Errorf("one event: %d", n)
	}
}

// Every privilege column of mysql.user is one the account check reads.
func TestGlobalPrivilegeColumnsMatchTheServer(t *testing.T) {
	d := rootDeps(t)
	if _, err := Install(context.Background(), d); err != nil {
		t.Fatalf("Install: %v", err)
	}
	cols := strings.Fields(mustQuery(t, d, "SELECT GROUP_CONCAT(COLUMN_NAME SEPARATOR ' ') FROM information_schema.COLUMNS WHERE TABLE_SCHEMA = 'mysql' AND TABLE_NAME = 'user' AND COLUMN_NAME LIKE '%\\_priv';"))
	if !slices.Equal(slices.Sorted(slices.Values(cols)), slices.Sorted(slices.Values(globalPrivilegeColumns))) {
		t.Errorf("mysql.user's privilege columns\n%v\ndiffer from the ones checked\n%v", cols, globalPrivilegeColumns)
	}
}
