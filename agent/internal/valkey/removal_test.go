package valkey

import (
	"context"
	"errors"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"zimam/agent/internal/sysexec"
)

// The call carries the cursor, the pattern and how many pages to scan; the
// script is fixed text.
func TestPurgeArgs(t *testing.T) {
	a := PurgeArgs("42", "shop")
	if len(a) != 6 || a[0] != "EVAL" || a[1] != purgeScript || a[2] != "0" || a[3] != "42" || a[4] != "s_shop:*" || a[5] != "5" {
		t.Errorf("args = %q", a)
	}
}

// removal is a server with users for shop and blog, and an in-memory stand-in
// for the progress and hash that the caller keeps in the site's state.
type removal struct {
	e        *testEnv
	st       states
	prog     Progress
	saves    []Progress
	events   []string
	failSave bool
}

// The two server processes a restart tells apart.
var (
	runA = strings.Repeat("a", 40)
	runB = strings.Repeat("b", 40)
)

// databases is the answer to the query for the number of databases.
func databases(n int) sysexec.Script {
	return live("databases\n"+strconv.Itoa(n)+"\n", "CONFIG", "GET", "databases")
}

func infoRun(id string) sysexec.Script {
	return live("# Server\r\nvalkey_version:9.0.4\r\nrun_id:"+id+"\r\ntcp_port:6379\r\n", "INFO", "server")
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
	r.e.add(infoRun(runA), databases(1))
	r.e.add(scripts...)
	return r
}

func (r *removal) rem(t *testing.T) Removal {
	return Removal{
		Load: func() (Progress, error) { return r.prog, nil },
		Save: func(p Progress) error {
			if r.failSave {
				return errors.New("disk full")
			}
			r.saves = append(r.saves, p)
			r.prog = p
			return nil
		},
		Clear: func() error {
			r.events = append(r.events, "clear; users.acl has s_shop: "+boolText(strings.Contains(readFile(t, r.e.p.ValkeyACL()), "user s_shop ")))
			delete(r.st.hash, "shop")
			r.prog = Progress{}
			return nil
		},
	}
}

func (r *removal) run(t *testing.T) error {
	t.Helper()
	return RemoveSiteUser(context.Background(), r.e.d, r.st.users, "shop", r.rem(t))
}

// again gives the server a new set of scripts, as a later run finds it.
func (r *removal) again(scripts ...sysexec.Script) {
	r.e.run = sysexec.NewFakeRunner(append([]sysexec.Script{infoRun(runA), databases(1)}, scripts...)...)
	r.e.srv = serverRunner{FakeRunner: r.e.run, killed: r.e.killed}
	r.e.d.Run = r.e.srv
}

func boolText(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

// cursorOfFirstPurge is the cursor argument of the first purge call made.
func cursorOfFirstPurge(r *removal) string {
	for _, c := range r.e.run.Calls() {
		if i := slices.Index(c.Args, "EVAL"); i >= 0 {
			return c.Args[i+3]
		}
	}
	return ""
}

func TestRemoveSiteUserOrder(t *testing.T) {
	step := purgeStep("0", "shop", "0 4\n")
	r := newRemoval(t, step, loadOK)
	if err := r.run(t); err != nil {
		t.Fatal(err)
	}
	want := []string{line(infoRun(runA)), line(databases(1)), line(step), line(loadOK), line(step)}
	if got := lines(r.e.run.Calls()); !slices.Equal(got, want) {
		t.Errorf("ran\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	// Pass 2 is recorded once the user is removed, the state is cleared last
	// with the live users already without the site, and nothing is left saved.
	if !slices.Equal(r.saves, []Progress{{2, "0", 0, runA}}) || r.prog != (Progress{}) {
		t.Errorf("saves %v, left %v", r.saves, r.prog)
	}
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
	first := purgeStep("0", "shop", "0 4\n")
	boom := failing(purgeStep("0", "shop", ""), 1, "ERR boom\n")
	for name, tc := range map[string]struct {
		scripts []sysexec.Script
		calls   int
	}{
		"first purge": {[]sysexec.Script{boom}, 3},
		"load":        {[]sysexec.Script{first, loadFail, loadOK}, 5},
		"last purge":  {[]sysexec.Script{first, loadOK, boom}, 5},
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
			r.again(purgeStep("0", "shop", "0 0\n"), purgeStep("0", "shop", "0 0\n"), loadOK)
			if err := r.run(t); err != nil {
				t.Fatal(err)
			}
			if len(r.events) != 1 || strings.Contains(readFile(t, r.e.p.ValkeyACL()), "s_shop") || r.prog != (Progress{}) {
				t.Errorf("the second run didn't end: events %q, progress %v", r.events, r.prog)
			}
		})
	}
}

// A purge that is out of time saves the cursor it reached; the next run
// starts from it, not from the beginning, and ends the removal.
func TestRemoveSiteUserResumesFromTheSavedCursor(t *testing.T) {
	old := purgeBudget
	purgeBudget = 0 // one call per run
	defer func() { purgeBudget = old }()
	r := newRemoval(t, purgeStep("0", "shop", "70 100\n"))
	err := r.run(t)
	if err == nil || !strings.Contains(err.Error(), "run the command again to continue") {
		t.Fatalf("RemoveSiteUser = %v", err)
	}
	if r.prog != (Progress{1, "70", 0, runA}) || len(r.events) != 0 {
		t.Fatalf("progress %v, events %q", r.prog, r.events)
	}
	// Second run: starts at 70, not 0, and runs out of time again.
	r.again(purgeStep("70", "shop", "90 100\n"))
	if err := r.run(t); err == nil || r.prog != (Progress{1, "90", 0, runA}) || cursorOfFirstPurge(r) != "70" {
		t.Fatalf("second run: %v, progress %v, first cursor %q", err, r.prog, cursorOfFirstPurge(r))
	}
	// Third run: the pass ends, the user is removed, pass 2 starts at 0 and
	// runs out of time at once.
	r.again(purgeStep("90", "shop", "0 5\n"), loadOK, purgeStep("0", "shop", "33 7\n"))
	if err := r.run(t); err == nil || r.prog != (Progress{2, "33", 0, runA}) || cursorOfFirstPurge(r) != "90" {
		t.Fatalf("third run: %v, progress %v", err, r.prog)
	}
	if strings.Contains(readFile(t, r.e.p.ValkeyACL()), "s_shop") {
		t.Error("the user is not removed after pass 1")
	}
	// Fourth run: pass 2 resumes from 33 (no pass 1 call), the users are loaded
	// again first, and the end clears the progress.
	r.again(loadOK, purgeStep("33", "shop", "0 2\n"))
	if err := r.run(t); err != nil {
		t.Fatal(err)
	}
	got := lines(r.e.run.Calls())
	if len(got) != 4 || got[2] != line(loadOK) || got[3] != line(purgeStep("33", "shop", "")) {
		t.Errorf("fourth run made\n%s", strings.Join(got, "\n"))
	}
	if r.prog != (Progress{}) || len(r.events) != 1 {
		t.Errorf("progress %v, events %q", r.prog, r.events)
	}
}

func TestRemoveSiteUserWhoseProgressCantBeSaved(t *testing.T) {
	old := purgeBudget
	purgeBudget = 0
	defer func() { purgeBudget = old }()
	r := newRemoval(t, purgeStep("0", "shop", "70 100\n"))
	r.failSave = true
	err := r.run(t)
	if err == nil || !strings.Contains(err.Error(), "could not be saved") || !strings.Contains(err.Error(), "disk full") {
		t.Errorf("RemoveSiteUser = %v", err)
	}
}

func TestRemoveSiteUserRefusesProgressItCannotUse(t *testing.T) {
	for _, p := range []Progress{{3, "0", 0, ""}, {1, "", 0, ""}, {2, "x", 0, ""}, {-1, "0", 0, ""}, {1, "5", -1, ""}} {
		r := newRemoval(t)
		r.prog = p
		if err := r.run(t); err == nil || len(r.e.run.Calls()) != 0 {
			t.Errorf("%v: %v, %d calls", p, err, len(r.e.run.Calls()))
		}
	}
}

func TestRemoveSiteUserClearFailureIsRunAgain(t *testing.T) {
	r := newRemoval(t, purgeStep("0", "shop", "0 0\n"), loadOK)
	rem := r.rem(t)
	rem.Clear = func() error { return errors.New("disk full") }
	err := RemoveSiteUser(context.Background(), r.e.d, r.st.users, "shop", rem)
	if err == nil || !strings.Contains(err.Error(), "run the command again") || !strings.Contains(err.Error(), "disk full") {
		t.Errorf("RemoveSiteUser = %v", err)
	}
}

// A cancelled job saves the cursor and says the removal was interrupted.
func TestRemoveSiteUserInterruptedByACancel(t *testing.T) {
	r := newRemoval(t, purgeStep("0", "shop", "70 100\n"), purgeStep("70", "shop", "0 1\n"))
	ctx, cancel := context.WithCancel(context.Background())
	r.e.srv.onCall = func(c sysexec.Cmd, _ context.Context) error {
		if slices.Contains(c.Args, "EVAL") {
			cancel()
		}
		return nil
	}
	r.e.d.Run = r.e.srv
	old := purgeBudget
	purgeBudget = time.Hour
	defer func() { purgeBudget = old }()
	err := RemoveSiteUser(ctx, r.e.d, r.st.users, "shop", r.rem(t))
	if err == nil || !strings.Contains(err.Error(), "interrupted") || !strings.Contains(err.Error(), "run the command again to continue") || !errors.Is(err, context.Canceled) {
		t.Fatalf("RemoveSiteUser = %v", err)
	}
	if r.prog != (Progress{1, "70", 0, runA}) {
		t.Errorf("progress %v", r.prog)
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
