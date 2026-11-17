package mysql

import (
	"context"
	"os"
	"reflect"
	"strings"
	"testing"

	"zimam/agent/internal/paths"
	"zimam/agent/internal/services"
	"zimam/agent/internal/sysexec"
)

func TestSizesParsesTheQueryRows(t *testing.T) {
	run := sysexec.NewFakeRunner(sysexec.Script{Path: clientPath, Args: clientArgs,
		Result: sysexec.Result{Stdout: []byte("information_schema\t0\ns_shop_empty\t0\ns_shop_main\t6848512\n")}})
	got, err := Sizes(context.Background(), sysexec.Deps{Run: run})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]int64{"information_schema": 0, "s_shop_empty": 0, "s_shop_main": 6848512}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Sizes = %v, want %v", got, want)
	}
}

func TestSizesRefusesRowsItCannotRead(t *testing.T) {
	for _, out := range []string{"s_shop_a\n", "s_shop_a\tmany\n", "s_shop_a\t-1\n"} {
		run := sysexec.NewFakeRunner(sysexec.Script{Path: clientPath, Args: clientArgs, Result: sysexec.Result{Stdout: []byte(out)}})
		if _, err := Sizes(context.Background(), sysexec.Deps{Run: run}); err == nil {
			t.Errorf("Sizes accepted %q", out)
		}
	}
}

func TestInstalledReadsServicesState(t *testing.T) {
	p := paths.Paths{Root: t.TempDir()}
	d := sysexec.Deps{Paths: p}
	if ok, err := Installed(d); err != nil || ok {
		t.Fatalf("no services.json: %v, %v", ok, err)
	}
	if err := services.Update(p, func(s *services.State) { s.MySQL = services.Entry{Installed: true, Version: "8.4.13"} }); err != nil {
		t.Fatal(err)
	}
	if ok, err := Installed(d); err != nil || !ok {
		t.Fatalf("installed: %v, %v", ok, err)
	}
	if err := os.WriteFile(p.ServicesState(), []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Installed(d); err == nil {
		t.Error("a broken services.json read as a clean answer")
	}
}

func TestObjectCount(t *testing.T) {
	run := sysexec.NewFakeRunner(sysexec.Script{Path: clientPath, Args: clientArgs, Result: sysexec.Result{Stdout: []byte("3\n")}})
	d := sysexec.Deps{Run: run}
	if n, err := ObjectCount(context.Background(), d, "s_shop_a"); err != nil || n != 3 {
		t.Fatalf("ObjectCount = %d, %v", n, err)
	}
	sql := string(run.Calls()[0].Stdin)
	for _, want := range []string{"information_schema.tables WHERE table_schema = 's_shop_a'", "information_schema.routines WHERE routine_schema = 's_shop_a'", "information_schema.events WHERE event_schema = 's_shop_a'"} {
		if !strings.Contains(sql, want) {
			t.Errorf("SQL lacks %q:\n%s", want, sql)
		}
	}
	if _, err := ObjectCount(context.Background(), d, "a'b"); err == nil || len(run.Calls()) != 1 {
		t.Errorf("a bad name reached the client: %v", err)
	}
}
