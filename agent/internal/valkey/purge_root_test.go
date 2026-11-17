//go:build roottest

package valkey

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"
)

// memRemoval keeps a removal's progress and hash in memory, as the site's
// state would.
func memRemoval(st *states, site string, prog *Progress) Removal {
	return Removal{
		Load: func() (Progress, error) { return *prog, nil },
		Save: func(p Progress) error { *prog = p; return nil },
		Clear: func() error {
			delete(st.hash, site)
			*prog = Progress{}
			return nil
		},
	}
}

// A keyspace too large for one run is removed after several: each run saves
// where it got to and the next continues, and only the site's keys go. Then a
// full purge is timed over a million keys of other prefixes.
func TestRootPurgeLargeKeyspace(t *testing.T) {
	d := rootDeps(t)
	ctx := context.Background()
	var st states
	if ok, err := Installed(d); err != nil {
		t.Fatal(err)
	} else if !ok {
		if _, err := Install(ctx, d, st.users); err != nil {
			t.Fatalf("Install: %v", err)
		}
	}
	admin := func(args ...string) string {
		t.Helper()
		out, err := cliAdmin(ctx, d, args...)
		if err != nil {
			t.Fatalf("%v: %v", args[:min(len(args), 2)], err)
		}
		return out
	}
	fill := func(prefix string, n int) {
		t.Helper()
		for done := 0; done < n; done += 250000 {
			chunk := min(250000, n-done)
			admin("EVAL", "for i = 1, tonumber(ARGV[2]) do redis.call('SET', ARGV[1] .. (tonumber(ARGV[3]) + i), 1) end return 1", "0", prefix, strconv.Itoa(chunk), strconv.Itoa(done))
		}
	}
	dbsize := func() int {
		n, err := strconv.Atoi(admin("DBSIZE"))
		if err != nil {
			t.Fatal(err)
		}
		return n
	}

	t.Run("several runs", func(t *testing.T) {
		admin("FLUSHALL")
		if err := ApplyUsers(ctx, d, st.users, st.setting("shop", passA1)); err != nil {
			t.Fatal(err)
		}
		fill("other:bulk:", 500000)
		fill("s_shop:bulk:", 5000)
		fill("s_shop-x:bulk:", 1000)
		before := dbsize()
		oldBudget, oldPages := purgeBudget, purgePages
		purgeBudget, purgePages = 5*time.Millisecond, 1 // a run is a few calls
		defer func() { purgeBudget, purgePages = oldBudget, oldPages }()
		var prog Progress
		runs := 0
		start := time.Now()
		for {
			runs++
			err := RemoveSiteUser(ctx, d, st.users, "shop", memRemoval(&st, "shop", &prog))
			if err == nil {
				break
			}
			if runs > 1000 || !strings.Contains(err.Error(), "run the command again to continue") {
				t.Fatalf("run %d: %v", runs, err)
			}
		}
		t.Logf("removed the site in %d runs, %s (progress is saved between runs)", runs, time.Since(start))
		if runs < 5 {
			t.Errorf("only %d runs: the budget did not split the work", runs)
		}
		if got := admin("EVAL", "return #redis.call('KEYS', ARGV[1])", "0", "s_shop:*"); got != "0" {
			t.Errorf("%s keys s_shop:* remain", got)
		}
		if got := dbsize(); got != before-5000 {
			t.Errorf("DBSIZE %d, want %d: exactly the site's 5000 keys must go", got, before-5000)
		}
		if got := admin("EVAL", "return #redis.call('KEYS', ARGV[1])", "0", "s_shop-x:*"); got != "1000" {
			t.Errorf("s_shop-x:* has %s keys, want 1000", got)
		}
		if prog != (Progress{}) {
			t.Errorf("progress left: %v", prog)
		}
	})

	t.Run("one million keys", func(t *testing.T) {
		admin("FLUSHALL")
		fill("other:bulk:", 1000000)
		fill("s_shop:bulk:", 5000)
		start := time.Now()
		n, err := PurgeSiteKeys(ctx, d, "shop")
		took := time.Since(start)
		if err != nil || n != 5000 {
			t.Fatalf("PurgeSiteKeys = %d, %v", n, err)
		}
		t.Logf("a full purge over 1,005,000 keys (5,000 of the site's) took %s with the real budget and pages", took)
		if got := dbsize(); got != 1000000 {
			t.Errorf("DBSIZE %d, want 1000000", got)
		}
	})
}

// A cursor is good only for the process that gave it. The purge is stopped
// partway through its second pass, the real server is restarted (it chooses a
// new hash seed and rebuilds its tables), and the removal is run again: it must
// rescan from the start and leave none of the site's keys.
func TestRootPurgeAfterARestart(t *testing.T) {
	d := rootDeps(t)
	ctx := context.Background()
	var st states
	if ok, err := Installed(d); err != nil {
		t.Fatal(err)
	} else if !ok {
		if _, err := Install(ctx, d, st.users); err != nil {
			t.Fatalf("Install: %v", err)
		}
	}
	admin := func(args ...string) string {
		t.Helper()
		out, err := cliAdmin(ctx, d, args...)
		if err != nil {
			t.Fatalf("%v: %v", args[:min(len(args), 2)], err)
		}
		return out
	}
	admin("FLUSHALL")
	if err := ApplyUsers(ctx, d, st.users, st.setting("shop", passA1)); err != nil {
		t.Fatal(err)
	}
	fill := func(prefix string, n int) {
		admin("EVAL", "for i = 1, tonumber(ARGV[2]) do redis.call('SET', ARGV[1] .. i, 1) end return 1", "0", prefix, strconv.Itoa(n))
	}
	fill("other:bulk:", 200000)
	fill("s_shop:bulk:", 20000)
	count := func(pattern string) int {
		n, err := strconv.Atoi(admin("EVAL", "local n = 0 local c = '0' repeat local r = redis.call('SCAN', c, 'MATCH', ARGV[1], 'COUNT', 10000) c = r[1] n = n + #r[2] until c == '0' return n", "0", pattern))
		if err != nil {
			t.Fatal(err)
		}
		return n
	}

	oldBudget, oldPages := purgeBudget, purgePages
	purgeBudget, purgePages = 5*time.Millisecond, 1
	defer func() { purgeBudget, purgePages = oldBudget, oldPages }()
	var prog Progress
	runs := 0
	step := func(until func() bool) {
		for !until() {
			runs++
			if err := RemoveSiteUser(ctx, d, st.users, "shop", memRemoval(&st, "shop", &prog)); err == nil || runs > 1000 {
				t.Fatalf("run %d: stopped with %v before the second pass was partway", runs, err)
			}
		}
	}
	step(func() bool { return prog.Pass == 2 })
	// Keys written after the first pass, before the user went: the second pass
	// exists to find them.
	fill("s_shop:late:", 20000)
	step(func() bool { return prog.Cursor != "0" })
	before := prog
	left := count("s_shop:*")
	t.Logf("stopped in pass 2 at cursor %s after %d runs; %d of the site's keys are left", before.Cursor, runs, left)
	if left == 0 {
		t.Fatal("no key left to find after the restart")
	}

	admin("SAVE")
	shell(t, "/usr/bin/systemctl", "restart", unit)
	for i := 0; ; i++ {
		if _, err := cliAdmin(ctx, d, "PING"); err == nil {
			break
		}
		if i > 100 {
			t.Fatal("Valkey did not come back")
		}
		time.Sleep(200 * time.Millisecond)
	}
	if got := count("s_shop:*"); got != left {
		t.Fatalf("%d site keys after the restart, want %d (the data must have been kept)", got, left)
	}
	for {
		runs++
		err := RemoveSiteUser(ctx, d, st.users, "shop", memRemoval(&st, "shop", &prog))
		if err == nil {
			break
		}
		if runs > 2000 {
			t.Fatalf("run %d: %v", runs, err)
		}
		if prog.RunID == before.RunID {
			t.Fatalf("the progress still carries the old run id after a run on the restarted server: %v", err)
		}
	}
	if got := count("s_shop:*"); got != 0 {
		t.Errorf("%d of the site's keys remain after the restart and the second removal", got)
	}
	if got := count("other:bulk:*"); got != 200000 {
		t.Errorf("%d other keys, want 200000", got)
	}
	if prog != (Progress{}) {
		t.Errorf("progress left: %v", prog)
	}
}
