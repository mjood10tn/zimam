package mysql

import (
	"context"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"zimam/agent/contract"
	"zimam/agent/internal/sysexec"
)

const testPassword contract.ServicePassword = "Abcdefghijklmnopqrstuvwx-_0123456789"

func TestStatementsQuoteIdentifiers(t *testing.T) {
	both := "'s_my-shop'@'localhost', 's_my-shop'@'127.0.0.1'"
	for _, tc := range []struct {
		name string
		st   Statement
		want string
	}{
		{"create database", CreateDatabase("s_my_shop_blog"),
			"CREATE DATABASE `s_my_shop_blog` CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci;"},
		{"drop database", DropDatabase("s_my_shop_blog"),
			"DROP DATABASE IF EXISTS `s_my_shop_blog`;"},
		{"create user", CreateUser("my-shop", testPassword),
			"CREATE USER IF NOT EXISTS 's_my-shop'@'localhost' IDENTIFIED BY '" + string(testPassword) + "', 's_my-shop'@'127.0.0.1' IDENTIFIED BY '" + string(testPassword) + "';"},
		{"alter user", AlterUserPassword("my-shop", testPassword),
			"ALTER USER 's_my-shop'@'localhost' IDENTIFIED BY '" + string(testPassword) + "', 's_my-shop'@'127.0.0.1' IDENTIFIED BY '" + string(testPassword) + "';"},
		// _ is a wildcard in a grant's database name; the escape makes it
		// one character. The name's backslashes stay out of the other
		// statements, where a database name is not a pattern.
		{"grant", GrantAll("my-shop", "s_my_shop_blog"),
			"GRANT ALL PRIVILEGES ON `s" + `\_my\_shop\_blog` + "`.* TO " + both + ";"},
		{"revoke", RevokeAll("my-shop", "s_my_shop_blog"),
			"REVOKE IF EXISTS ALL PRIVILEGES ON `s" + `\_my\_shop\_blog` + "`.* FROM " + both + " IGNORE UNKNOWN USER;"},
		{"drop user", DropUser("my-shop"), "DROP USER IF EXISTS " + both + ";"},
		{"sizes", DatabaseSizes(),
			"SET SESSION information_schema_stats_expiry = 0;\n" +
				"SELECT s.schema_name, COALESCE(SUM(t.data_length + t.index_length), 0) FROM information_schema.schemata s " +
				"LEFT JOIN information_schema.tables t ON t.table_schema = s.schema_name GROUP BY s.schema_name ORDER BY s.schema_name;"},
	} {
		if tc.st.err != nil {
			t.Errorf("%s: %v", tc.name, tc.st.err)
			continue
		}
		if tc.st.sql != tc.want {
			t.Errorf("%s:\n got %s\nwant %s", tc.name, tc.st.sql, tc.want)
		}
	}
}

// A value that did not pass its type's Validate must not reach the SQL, even
// when a caller built it by conversion.
func TestStatementWithUnvalidatedValueIsRefused(t *testing.T) {
	short := contract.ServicePassword("x'; DROP DATABASE mysql; --")
	for name, st := range map[string]Statement{
		"backtick in a database name": CreateDatabase("a`b"),
		"reserved database name":      DropDatabase("mysql"),
		"quote in a password":         CreateUser("shop", short),
		"short password":              AlterUserPassword("shop", "short"),
		"quote in a site name":        DropUser("a'b"),
		"bad grant database":          GrantAll("shop", "x' OR '1"),
		"bad revoke site":             RevokeAll("Shop", "s_shop_x"),
	} {
		run := sysexec.NewFakeRunner()
		_, err := Exec(context.Background(), sysexec.Deps{Run: run}, st)
		if err == nil {
			t.Errorf("%s: Exec accepted it", name)
		}
		if n := len(run.Calls()); n != 0 {
			t.Errorf("%s: %d calls reached the client", name, n)
		}
	}
	// One bad statement stops the whole batch before the first runs.
	run := sysexec.NewFakeRunner()
	_, err := Exec(context.Background(), sysexec.Deps{Run: run}, CreateDatabase("s_shop_a"), DropDatabase("a`b"))
	if err == nil || len(run.Calls()) != 0 {
		t.Errorf("batch with a bad statement: err %v, %d calls", err, len(run.Calls()))
	}
}

// The package gives no way to hand it SQL: no exported function or method
// takes a string, and a Statement has no exported field and no method that
// returns its text.
func TestNoRawStatementConstructor(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil || len(files) == 0 {
		t.Fatalf("Glob: %v, %v", files, err)
	}
	fset := token.NewFileSet()
	checked := 0
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range f.Decls {
			switch d := decl.(type) {
			case *ast.FuncDecl:
				if !d.Name.IsExported() {
					continue
				}
				checked++
				for _, field := range d.Type.Params.List {
					if hasString(field.Type) {
						t.Errorf("%s: exported %s takes a string-typed parameter", name, d.Name.Name)
					}
				}
			case *ast.GenDecl:
				for _, spec := range d.Specs {
					ts, ok := spec.(*ast.TypeSpec)
					if !ok || !ts.Name.IsExported() {
						continue
					}
					if st, ok := ts.Type.(*ast.StructType); ok {
						for _, field := range st.Fields.List {
							for _, n := range field.Names {
								if n.IsExported() && ts.Name.Name == "Statement" {
									t.Errorf("Statement has an exported field %s", n.Name)
								}
							}
						}
					}
				}
			}
		}
	}
	if checked < 10 {
		t.Errorf("checked only %d exported functions", checked)
	}
	typ := reflect.TypeOf(Statement{})
	for i := range typ.NumMethod() {
		m := typ.Method(i)
		if m.Name != "String" && m.Name != "GoString" {
			t.Errorf("Statement has the exported method %s", m.Name)
		}
	}
}

func hasString(e ast.Expr) bool {
	switch t := e.(type) {
	case *ast.Ident:
		return t.Name == "string"
	case *ast.ArrayType:
		return hasString(t.Elt)
	case *ast.Ellipsis:
		return hasString(t.Elt)
	case *ast.MapType:
		return hasString(t.Key) || hasString(t.Value)
	}
	return false
}

// A statement that holds a password prints as its kind, in every format
// verb, so a log line or an error can't carry it.
func TestStatementDoesNotPrintItsSQL(t *testing.T) {
	st := CreateUser("shop", testPassword)
	for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%q"} {
		if got := fmt.Sprintf(verb, st); strings.Contains(got, string(testPassword)) || strings.Contains(got, "IDENTIFIED") {
			t.Errorf("%s prints %q", verb, got)
		}
	}
	if got := fmt.Sprint(st); got != "create user" {
		t.Errorf("Sprint = %q, want the kind", got)
	}
}

func TestExecRunsEachStatementThroughTheRootClient(t *testing.T) {
	run := sysexec.NewFakeRunner(sysexec.Script{Path: clientPath, Args: clientArgs, Result: sysexec.Result{Stdout: []byte("a\t1\nb\t2\n")}})
	d := sysexec.Deps{Run: run}
	rows, err := Exec(context.Background(), d, CreateDatabase("s_shop_a"), DatabaseSizes())
	if err != nil {
		t.Fatal(err)
	}
	calls := run.Calls()
	if len(calls) != 2 {
		t.Fatalf("%d client runs, want one per statement", len(calls))
	}
	if got := string(calls[0].Stdin); got != CreateDatabase("s_shop_a").sql+"\n" {
		t.Errorf("stdin = %q", got)
	}
	// Both statements answer the same scripted rows; the rows of all
	// statements come back in order.
	if want := [][]string{{"a", "1"}, {"b", "2"}, {"a", "1"}, {"b", "2"}}; !reflect.DeepEqual(rows, want) {
		t.Errorf("rows = %q, want %q", rows, want)
	}
	if rows, err := Exec(context.Background(), d); err != nil || rows != nil || len(run.Calls()) != 2 {
		t.Errorf("Exec with no statements: %v, %v", rows, err)
	}
}

func TestExecStopsAtTheFirstFailureAndKeepsSecretsOut(t *testing.T) {
	stderr := "ERROR 1396 (HY000) at line 1: Operation CREATE USER failed for 's_shop'@'localhost' IDENTIFIED BY '" + string(testPassword) + "'"
	run := sysexec.NewFakeRunner(
		failing(sysexec.Script{Path: clientPath, Args: clientArgs}, 1, stderr),
	)
	_, err := Exec(context.Background(), sysexec.Deps{Run: run}, CreateUser("shop", testPassword), DropUser("shop"))
	if err == nil {
		t.Fatal("no error")
	}
	if len(run.Calls()) != 1 {
		t.Errorf("%d runs after a failure, want 1", len(run.Calls()))
	}
	for _, text := range []string{err.Error(), fmt.Sprintf("%+v", err), fmt.Sprintf("%#v", err)} {
		if strings.Contains(text, string(testPassword)) {
			t.Errorf("the password is in the error: %s", text)
		}
	}
	var e *Error
	if !errors.As(err, &e) || e.Errno() != 1396 || e.Kind() != "create user" {
		t.Fatalf("error = %#v, want errno 1396 of a create user", err)
	}
	var exit *sysexec.ExitError
	if errors.As(err, &exit) {
		t.Error("the error unwraps to the client's exit error, which holds stderr")
	}
}

func TestExecErrorShowsClientMessageForStatementsWithoutSecrets(t *testing.T) {
	run := sysexec.NewFakeRunner(failing(sysexec.Script{Path: clientPath, Args: clientArgs}, 1,
		"ERROR 1007 (HY000) at line 1: Can't create database 's_shop_a'; database exists"))
	_, err := Exec(context.Background(), sysexec.Deps{Run: run}, CreateDatabase("s_shop_a"))
	var e *Error
	if !errors.As(err, &e) || e.Errno() != ErrDatabaseExists {
		t.Fatalf("error = %v", err)
	}
	if !strings.Contains(err.Error(), "database exists") || !strings.Contains(err.Error(), "create database") {
		t.Errorf("message %q", err)
	}
}

func TestExecDownServer(t *testing.T) {
	for _, stderr := range []string{
		"ERROR 2002 (HY000): Can't connect to local MySQL server through socket '/var/run/mysqld/mysqld.sock' (2)",
		"ERROR 2013 (HY000): Lost connection to MySQL server at 'reading initial communication packet'",
	} {
		run := sysexec.NewFakeRunner(failing(sysexec.Script{Path: clientPath, Args: clientArgs}, 1, stderr))
		_, err := Exec(context.Background(), sysexec.Deps{Run: run}, DatabaseSizes())
		if !IsDown(err) {
			t.Errorf("IsDown(%v) = false", err)
		}
	}
	if IsDown(errors.New("x")) || IsDown(nil) {
		t.Error("IsDown on an unrelated error")
	}
}
