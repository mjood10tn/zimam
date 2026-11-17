package valkey

import (
	"context"
	"path"
	"strings"
	"testing"
	"time"

	"zimam/agent/internal/sysexec"
)

func purgeStep(cursor, site, reply string) sysexec.Script {
	return live(reply, PurgeArgs(cursor, contractName(site))...)
}

func TestEscapeGlob(t *testing.T) {
	for in, want := range map[string]string{
		"shop":    "shop",
		"shop-x":  "shop-x",
		"a*b":     `a\*b`,
		"a?b[c]d": `a\?b\[c\]d`,
		`a\b`:     `a\\b`,
	} {
		if got := escapeGlob(in); got != want {
			t.Errorf("escapeGlob(%q) = %q, want %q", in, got, want)
		}
	}
}

// The colon after the name keeps one site's prefix apart from another that
// starts with the same letters.
func TestPatternOfOneSiteMissesTheNext(t *testing.T) {
	p := sitePattern("shop")
	if p != "s_shop:*" {
		t.Fatalf("pattern %q", p)
	}
	for key, want := range map[string]bool{
		"s_shop:a": true, "s_shop:with space": true, "s_shop:line\nbreak": true, "s_shop:": true,
		"s_shop-x:a": false, "s_shopx:a": false, "s_sho:a": false, "other:s_shop:a": false,
	} {
		if got, _ := path.Match(p, key); got != want {
			t.Errorf("%q matches %q = %v, want %v", p, key, got, want)
		}
	}
}

func TestPurgePagesUntilTheCursorIsZero(t *testing.T) {
	e := cliEnv(t)
	e.add(databases(1))
	e.add(purgeStep("0", "shop", "17 1000\n"), purgeStep("17", "shop", "9000000000000000000 400\n"), purgeStep("9000000000000000000", "shop", "0 5\n"))
	n, err := PurgeSiteKeys(context.Background(), e.d, "shop")
	if err != nil || n != 1405 {
		t.Fatalf("PurgeSiteKeys = %d, %v; want 1405", n, err)
	}
	if got := len(e.run.Calls()); got != 4 {
		t.Errorf("%d calls, want 4 (the databases, then three pages)", got)
	}
}

func TestPurgeRefusesWhatItCannotRead(t *testing.T) {
	for _, reply := range []string{"OK\n", "x 1\n", "0\n", "0 -1\n", "1 2 3\n", "0 1\nextra\n", "\n"} {
		e := cliEnv(t)
		e.add(databases(1))
		e.add(purgeStep("0", "shop", reply))
		if _, err := PurgeSiteKeys(context.Background(), e.d, "shop"); err == nil {
			t.Errorf("accepted the reply %q", reply)
		}
	}
	e := cliEnv(t)
	if _, err := PurgeSiteKeys(context.Background(), e.d, "Bad Name"); err == nil || len(e.run.Calls()) != 0 {
		t.Errorf("a bad name: %v, %d calls", err, len(e.run.Calls()))
	}
}

// A purge that doesn't finish in its time says to run again; what it removed
// stays removed.
func TestPurgeThatRunsOutOfTimeSaysRunAgain(t *testing.T) {
	e := cliEnv(t)
	e.add(databases(1))
	e.add(purgeStep("0", "shop", "5 10\n"), purgeStep("5", "shop", "6 10\n"), purgeStep("6", "shop", "7 10\n"), purgeStep("7", "shop", "8 10\n"))
	old := purgeBudget
	purgeBudget = 150 * time.Millisecond
	defer func() { purgeBudget = old }()
	e.srv.onCall = func(c sysexec.Cmd, _ context.Context) error { time.Sleep(80 * time.Millisecond); return nil }
	e.d.Run = e.srv
	n, err := PurgeSiteKeys(context.Background(), e.d, "shop")
	if err == nil || !strings.Contains(err.Error(), "run the command again") {
		t.Fatalf("PurgeSiteKeys = %d, %v", n, err)
	}
	if n < 10 {
		t.Errorf("removed %d, the pages done count", n)
	}
}
