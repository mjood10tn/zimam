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
		"grantees":         "SELECT User, Host, Db FROM mysql.db WHERE 's_shop_x_a' LIKE Db ORDER BY User, Host, Db;",
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
	out := "s_shop\tlocalhost\ts" + `\\` + "_blog" + `\\` + "_a\nplain\tx\ta" + `\t` + "b" + `\n` + "c\n"
	run := sysexec.NewFakeRunner(sysexec.Script{Path: clientPath, Args: clientArgs, Result: sysexec.Result{Stdout: []byte(out)}})
	rows, err := Exec(context.Background(), sysexec.Deps{Run: run}, DatabaseGrantees("s_blog_a"))
	if err != nil {
		t.Fatal(err)
	}
	want := [][]string{{"s_shop", "localhost", `s\_blog\_a`}, {"plain", "x", "a\tb\nc"}}
	if !reflect.DeepEqual(rows, want) {
		t.Errorf("rows = %q, want %q", rows, want)
	}
}

func TestGrantees(t *testing.T) {
	out := "s_shop\tlocalhost\ts" + `\\` + "_blog" + `\\` + "_a\ns_shop\t127.0.0.1\ts" + `\\` + "_blog" + `\\` + "_a\n"
	run := sysexec.NewFakeRunner(sysexec.Script{Path: clientPath, Args: clientArgs, Result: sysexec.Result{Stdout: []byte(out)}})
	got, err := Grantees(context.Background(), sysexec.Deps{Run: run}, "s_blog_a")
	if err != nil {
		t.Fatal(err)
	}
	want := []Grantee{{"s_shop", "localhost", `s\_blog\_a`}, {"s_shop", "127.0.0.1", `s\_blog\_a`}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Grantees = %v, want %v", got, want)
	}
	bad := sysexec.NewFakeRunner(sysexec.Script{Path: clientPath, Args: clientArgs, Result: sysexec.Result{Stdout: []byte("only\ttwo\n")}})
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
