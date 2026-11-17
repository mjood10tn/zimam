package valkey

import (
	"context"
	"errors"
	"os"
	"path"
	"slices"
	"strings"
	"testing"
	"time"

	"zimam/agent/internal/sysexec"
)

func purgeStep(cursor, pattern, reply string) sysexec.Script {
	return live(reply, "EVAL", PurgeScript, "0", cursor, pattern)
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
	e.add(purgeStep("0", "s_shop:*", "17 1000\n"), purgeStep("17", "s_shop:*", "9000000000000000000 400\n"), purgeStep("9000000000000000000", "s_shop:*", "0 5\n"))
	n, err := PurgeSiteKeys(context.Background(), e.d, "shop")
	if err != nil || n != 1405 {
		t.Fatalf("PurgeSiteKeys = %d, %v; want 1405", n, err)
	}
	if got := len(e.run.Calls()); got != 3 {
		t.Errorf("%d calls, want 3", got)
	}
}

func TestPurgeRefusesWhatItCannotRead(t *testing.T) {
	for _, reply := range []string{"OK\n", "x 1\n", "0\n", "0 -1\n", "1 2 3\n", "0 1\nextra\n", "\n"} {
		e := cliEnv(t)
		e.add(purgeStep("0", "s_shop:*", reply))
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
	e.add(purgeStep("0", "s_shop:*", "5 10\n"), purgeStep("5", "s_shop:*", "6 10\n"), purgeStep("6", "s_shop:*", "7 10\n"), purgeStep("7", "s_shop:*", "8 10\n"))
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

// removal is a server with users for shop and blog; events records what the
// caller's clear saw.
type removal struct {
	e      *testEnv
	st     states
	events []string
}

func newRemoval(t *testing.T, scripts ...sysexec.Script) *removal {
	t.Helper()
	r := &removal{e: applyEnv(t)}
	r.st.set("shop", "a")
	r.st.set("blog", "b")
	users, err := r.st.users()
	if err != nil {
		t.Fatal(err)
	}
	acl, err := RenderACL(hashOf(testPass), users)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(r.e.p.ValkeyACL(), acl, 0o640); err != nil {
		t.Fatal(err)
	}
	r.e.add(scripts...)
	return r
}

func (r *removal) run(t *testing.T) error {
	t.Helper()
	return RemoveSiteUser(context.Background(), r.e.d, r.st.users, "shop", func() error {
		r.events = append(r.events, "clear; users.acl has s_shop: "+boolText(strings.Contains(readFile(t, r.e.p.ValkeyACL()), "user s_shop ")))
		delete(r.st.hash, "shop")
		return nil
	})
}

func boolText(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

func TestRemoveSiteUserOrder(t *testing.T) {
	step := purgeStep("0", "s_shop:*", "0 4\n")
	r := newRemoval(t, step, loadOK)
	if err := r.run(t); err != nil {
		t.Fatal(err)
	}
	want := []string{line(step), line(loadOK), line(step)}
	if got := lines(r.e.run.Calls()); !slices.Equal(got, want) {
		t.Errorf("ran\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	// The state is cleared last, with the live users already without the site.
	if !slices.Equal(r.events, []string{"clear; users.acl has s_shop: no"}) {
		t.Errorf("events %q", r.events)
	}
	acl := readFile(t, r.e.p.ValkeyACL())
	if strings.Contains(acl, "s_shop") || !strings.Contains(acl, userLine("blog", "b")) {
		t.Errorf("users.acl =\n%s", acl)
	}
}

// A failure at each step leaves the hash in the state and the steps to run
// again; the second run ends the same way.
func TestRemoveSiteUserFailureAtEachStepConverges(t *testing.T) {
	first := purgeStep("0", "s_shop:*", "0 4\n")
	boom := failing(purgeStep("0", "s_shop:*", ""), 1, "ERR boom\n")
	for name, tc := range map[string]struct {
		scripts []sysexec.Script
		calls   int
	}{
		"first purge": {[]sysexec.Script{boom}, 1},
		"load":        {[]sysexec.Script{first, loadFail, loadOK}, 3},
		"last purge":  {[]sysexec.Script{first, loadOK, boom}, 3},
	} {
		t.Run(name, func(t *testing.T) {
			r := newRemoval(t, tc.scripts...)
			if err := r.run(t); err == nil {
				t.Fatal("RemoveSiteUser passed")
			}
			if len(r.events) != 0 || r.st.hash["shop"] == "" {
				t.Errorf("the hash was cleared (events %q) though a step failed", r.events)
			}
			if got := len(r.e.run.Calls()); got != tc.calls {
				t.Errorf("%d calls, want %d", got, tc.calls)
			}
			r.e.run = sysexec.NewFakeRunner(purgeStep("0", "s_shop:*", "0 0\n"), loadOK)
			r.e.srv = serverRunner{FakeRunner: r.e.run, killed: r.e.killed}
			r.e.d.Run = r.e.srv
			if err := r.run(t); err != nil {
				t.Fatal(err)
			}
			if len(r.events) != 1 || strings.Contains(readFile(t, r.e.p.ValkeyACL()), "s_shop") {
				t.Errorf("the second run didn't end: events %q", r.events)
			}
		})
	}
}

func TestRemoveSiteUserClearFailureIsRunAgain(t *testing.T) {
	r := newRemoval(t, purgeStep("0", "s_shop:*", "0 0\n"), loadOK)
	err := RemoveSiteUser(context.Background(), r.e.d, r.st.users, "shop", func() error { return errors.New("disk full") })
	if err == nil || !strings.Contains(err.Error(), "run the command again") || !strings.Contains(err.Error(), "disk full") {
		t.Errorf("RemoveSiteUser = %v", err)
	}
}

// It shares the lock with the install and the other applies.
func TestRemoveSiteUserWaitsForTheLock(t *testing.T) {
	r := newRemoval(t)
	unlock, err := sysexec.Lock(context.Background(), r.e.p.ValkeyACLLock())
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	old := lockWait
	lockWait = 100 * time.Millisecond
	defer func() { lockWait = old }()
	if err := r.run(t); err == nil || !strings.Contains(err.Error(), "try again") || len(r.e.run.Calls()) != 0 {
		t.Errorf("RemoveSiteUser = %v, %d calls", err, len(r.e.run.Calls()))
	}
}
