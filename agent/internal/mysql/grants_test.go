package mysql

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"zimam/agent/contract"
	"zimam/agent/internal/sysexec"
)

func TestGranteeStatementsGolden(t *testing.T) {
	both := "'s_shop'@'localhost', 's_shop'@'127.0.0.1'"
	got := map[string]Statement{
		"grantees":         DatabaseGrantees("s_shop_x_a"),
		"revoke unescaped": RevokeAllUnescaped("shop", "s_shop_x_a"),
	}
	want := map[string]string{
		"grantees":         "SET SESSION sql_mode = '';\nSELECT 'database' AS kind, User, Host, Db FROM mysql.db WHERE 's_shop_x_a' LIKE Db UNION ALL SELECT 'table', User, Host, Db FROM mysql.tables_priv WHERE 's_shop_x_a' LIKE Db UNION ALL SELECT 'routine', User, Host, Db FROM mysql.procs_priv WHERE 's_shop_x_a' LIKE Db UNION ALL SELECT 'column', User, Host, Db FROM mysql.columns_priv WHERE 's_shop_x_a' LIKE Db ORDER BY 2, 3, 4, 1;",
		"revoke unescaped": "REVOKE IF EXISTS ALL PRIVILEGES ON `s_shop_x_a`.* FROM " + both + " IGNORE UNKNOWN USER;",
	}
	for name, st := range got {
		if st.err != nil || st.sql != want[name] {
			t.Errorf("%s:\n got %s (%v)\nwant %s", name, st.sql, st.err, want[name])
		}
	}
}

// The client prints a backslash in a value as two. Rows come back as the
// server holds them.
func TestExecUnescapesBatchOutput(t *testing.T) {
	out := "database\ts_shop\tlocalhost\ts" + `\\` + "_blog" + `\\` + "_a\ntable\tplain\tx\ta" + `\t` + "b" + `\n` + "c\n"
	run := sysexec.NewFakeRunner(sysexec.Script{Path: clientPath, Args: clientArgs, Result: sysexec.Result{Stdout: []byte(out)}})
	rows, err := Exec(context.Background(), sysexec.Deps{Run: run}, DatabaseGrantees("s_blog_a"))
	if err != nil {
		t.Fatal(err)
	}
	want := [][]string{{"database", "s_shop", "localhost", `s\_blog\_a`}, {"table", "plain", "x", "a\tb\nc"}}
	if !reflect.DeepEqual(rows, want) {
		t.Errorf("rows = %q, want %q", rows, want)
	}
}

func TestGrantees(t *testing.T) {
	out := "database\ts_shop\tlocalhost\ts" + `\\` + "_blog" + `\\` + "_a\ntable\ts_shop\t127.0.0.1\ts" + `\\` + "_blog" + `\\` + "_a\n"
	run := sysexec.NewFakeRunner(sysexec.Script{Path: clientPath, Args: clientArgs, Result: sysexec.Result{Stdout: []byte(out)}})
	got, err := Grantees(context.Background(), sysexec.Deps{Run: run}, "s_blog_a")
	if err != nil {
		t.Fatal(err)
	}
	want := []Grantee{{"database", "s_shop", "localhost", `s\_blog\_a`}, {"table", "s_shop", "127.0.0.1", `s\_blog\_a`}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Grantees = %v, want %v", got, want)
	}
	bad := sysexec.NewFakeRunner(sysexec.Script{Path: clientPath, Args: clientArgs, Result: sysexec.Result{Stdout: []byte("only\tthree\tcolumns\n")}})
	if _, err := Grantees(context.Background(), sysexec.Deps{Run: bad}, "s_blog_a"); err == nil {
		t.Error("a row of two columns was accepted")
	}
	if _, err := Grantees(context.Background(), sysexec.Deps{Run: run}, "a'b"); err == nil || len(run.Calls()) != 1 {
		t.Errorf("a bad name reached the client: %v", err)
	}
}

// MySQL refuses a grant name over 64 characters once each underscore is
// escaped (error 1059), so the constructors don't build one.
func TestGrantStatementsRefuseNamesTooLongForAGrant(t *testing.T) {
	ok := contract.DatabaseName("s_aa_" + strings.Repeat("a", 57))      // 64 escaped
	tooLong := contract.DatabaseName("s_aa_" + strings.Repeat("a", 58)) // 65 escaped
	for name, mk := range map[string]func(contract.DatabaseName) Statement{
		"grant":            func(n contract.DatabaseName) Statement { return GrantAll("aa", n) },
		"revoke":           func(n contract.DatabaseName) Statement { return RevokeAll("aa", n) },
		"revoke unescaped": func(n contract.DatabaseName) Statement { return RevokeAllUnescaped("aa", n) },
	} {
		if st := mk(ok); st.err != nil || st.sql == "" {
			t.Errorf("%s: a name of 64 escaped characters: %v", name, st.err)
		}
		run := sysexec.NewFakeRunner()
		if _, err := Exec(context.Background(), sysexec.Deps{Run: run}, mk(tooLong)); err == nil || len(run.Calls()) != 0 {
			t.Errorf("%s: a name of 65 escaped characters: err %v, %d calls", name, err, len(run.Calls()))
		}
	}
}

func TestAccountPrivilegesStatement(t *testing.T) {
	st := SiteAccountPrivileges("shop")
	if st.err != nil {
		t.Fatal(st.err)
	}
	for _, want := range []string{
		"FROM mysql.user WHERE User = 's_shop' AND Host IN ('localhost', '127.0.0.1') AND 'Y' IN (Select_priv, Insert_priv,",
		"File_priv", "Super_priv", "Grant_priv", "Drop_role_priv)",
		"FROM mysql.global_grants WHERE USER = 's_shop'", "FROM mysql.role_edges WHERE TO_USER = 's_shop'", "FROM mysql.proxies_priv",
		"FROM mysql.db", "FROM mysql.tables_priv", "FROM mysql.procs_priv", "FROM mysql.columns_priv", "Grant_priv = 'Y'",
	} {
		if !strings.Contains(st.sql, want) {
			t.Errorf("the statement lacks %q:\n%s", want, st.sql)
		}
	}
	if bad := SiteAccountPrivileges("Sh'op"); bad.err == nil || bad.sql != "" {
		t.Error("an invalid site name made a statement")
	}
	out := "global\ts_shop\tlocalhost\t\ndatabase\ts_shop\t127.0.0.1\ts" + `\` + "_shop" + `\` + "_a\n"
	run := sysexec.NewFakeRunner(sysexec.Script{Path: clientPath, Args: clientArgs, Result: sysexec.Result{Stdout: []byte(out)}})
	got, err := AccountPrivileges(context.Background(), sysexec.Deps{Run: run}, "shop")
	if err != nil {
		t.Fatal(err)
	}
	want := []Privilege{{PrivGlobal, "s_shop", "localhost", ""}, {PrivDatabase, "s_shop", "127.0.0.1", `s\_shop\_a`}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("AccountPrivileges = %v, want %v", got, want)
	}
}
