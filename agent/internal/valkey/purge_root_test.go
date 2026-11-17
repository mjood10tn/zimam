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
