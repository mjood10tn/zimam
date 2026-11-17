//go:build roottest

package mysql

import (
	"context"
	"strings"
	"testing"

	"zimam/agent/internal/sysexec"
)

// What the grantee query returns for the three kinds of row a database can
// be covered by: a grant written with escaped underscores (as Zimam writes
// it), one with the name as it is, and a wildcard pattern. It must also leave
// out a grant on another database.
func TestGranteesAgainstRealMySQL(t *testing.T) {
	d := rootDeps(t)
	ctx := context.Background()
	if _, err := Install(ctx, d); err != nil {
		t.Fatalf("Install: %v", err)
	}
	cleanup := func() {
		queryQuiet(d, "DROP DATABASE IF EXISTS s_aa_grn;")
		for _, u := range []string{"h1", "h2", "h3", "h4"} {
			queryQuiet(d, "DROP USER IF EXISTS '"+u+"'@'localhost';")
		}
	}
	cleanup()
	t.Cleanup(cleanup)
	mustQuery(t, d, "CREATE DATABASE s_aa_grn;")
	for _, u := range []string{"h1", "h2", "h3", "h4"} {
		mustQuery(t, d, "CREATE USER '"+u+"'@'localhost';")
	}
	mustQuery(t, d, "GRANT ALL ON `s"+`\_aa\_grn`+"`.* TO 'h1'@'localhost';")
	mustQuery(t, d, "GRANT ALL ON `s_aa_grn`.* TO 'h2'@'localhost';")
	mustQuery(t, d, "GRANT ALL ON `s"+`\_aa\_`+"%`.* TO 'h3'@'localhost';")
	mustQuery(t, d, "GRANT ALL ON `s"+`\_bb\_x`+"`.* TO 'h4'@'localhost';")

	got, err := Grantees(ctx, d, "s_aa_grn")
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, g := range got {
		lines = append(lines, g.User+"@"+g.Host+" "+g.Pattern)
	}
	t.Logf("grantees of s_aa_grn: %q", lines)
	want := []string{`h1@localhost s\_aa\_grn`, `h2@localhost s_aa_grn`, `h3@localhost s\_aa\_%`}
	if strings.Join(lines, "|") != strings.Join(want, "|") {
		t.Errorf("grantees = %q, want %q", lines, want)
	}
}

// queryQuiet runs raw SQL for cleanup, where a failure doesn't matter.
func queryQuiet(d sysexec.Deps, sql string) { query(context.Background(), d, sql) }
