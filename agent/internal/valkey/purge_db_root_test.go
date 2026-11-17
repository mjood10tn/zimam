//go:build roottest

package valkey

import (
	"context"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

// asUserDB is asUser in a database the user SELECTed (valkey-cli -n); the
// password is in REDISCLI_AUTH.
func asUserDB(user, pass string, db int, args ...string) string {
	cmd := exec.Command("/usr/bin/valkey-cli", append([]string{"--no-auth-warning", "--user", user, "-h", "127.0.0.1", "-p", "6379", "-n", strconv.Itoa(db)}, args...)...)
	cmd.Env = append(os.Environ(), "REDISCLI_AUTH="+pass)
	out, _ := cmd.CombinedOutput()
	return strings.TrimSpace(string(out))
}

// A site's user may SELECT any database, and Laravel's stock cache store is
// database 1, so a destroyed site's keys must go from every database. Keys are
// written by the site's own user in databases 0, 1 and 15 (and by the admin in
// 5), a neighbour with a longer name writes in 0 and 1, and the removal runs in
// several steps with a tiny budget so that it crosses databases between runs.
func TestRootPurgeEveryDatabase(t *testing.T) {
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
	admin := func(db int, args ...string) string {
		t.Helper()
		out, err := cliAdminDB(ctx, d, db, args...)
		if err != nil {
			t.Fatalf("db %d %v: %v", db, args[:min(len(args), 2)], err)
		}
		return out
	}
	admin(0, "FLUSHALL")
	if err := ApplyUsers(ctx, d, st.users, st.setting("shop", passA1)); err != nil {
		t.Fatal(err)
	}
	if err := ApplyUsers(ctx, d, st.users, st.setting("shop-x", passB1)); err != nil {
		t.Fatal(err)
	}
	if n, err := databaseCount(ctx, d); err != nil || n != 16 {
		t.Fatalf("databases = %d, %v; want Valkey's default of 16", n, err)
	}

	for _, db := range []int{0, 1, 15} {
		for _, k := range []string{"s_shop:a", "s_shop:b", "s_shop:with space"} {
			if out := asUserDB("s_shop", passA1, db, "SET", k, "old"); out != "OK" {
				t.Fatalf("shop SET %q in %d: %q (its user can SELECT any database)", k, db, out)
			}
		}
	}
	admin(5, "SET", "s_shop:five", "old")
	admin(15, "SET", "s_shop:line\nbreak", "old")
	for _, db := range []int{0, 1} {
		for _, k := range []string{"s_shop-x:a", "s_shop-x:with space"} {
			if out := asUserDB("s_shop-x", passB1, db, "SET", k, "keep"); out != "OK" {
				t.Fatalf("shop-x SET %q in %d: %q", k, db, out)
			}
		}
	}
	// Many keys of other prefixes in several databases, so that a run with a
	// tiny budget ends inside a database.
	for _, db := range []int{0, 1, 2, 15} {
		admin(db, "EVAL", "for i = 1, 30000 do redis.call('SET', 'other:bulk:' .. i, 1) end return 1", "0")
	}
	count := func(db int, pattern string) int {
		n, err := strconv.Atoi(admin(db, "EVAL", "return #redis.call('KEYS', ARGV[1])", "0", pattern))
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	if count(1, "s_shop:*") != 3 || count(15, "s_shop:*") != 4 {
		t.Fatal("the keys of the site were not written where expected")
	}

	oldBudget, oldPages := purgeBudget, purgePages
	purgeBudget, purgePages = 5*time.Millisecond, 1
	defer func() { purgeBudget, purgePages = oldBudget, oldPages }()
	var prog Progress
	runs, maxDB := 0, 0
	for {
		runs++
		err := RemoveSiteUser(ctx, d, st.users, "shop", memRemoval(&st, "shop", &prog))
		if err == nil {
			break
		}
		if runs > 2000 || !strings.Contains(err.Error(), "run the command again to continue") {
			t.Fatalf("run %d: %v", runs, err)
		}
		maxDB = max(maxDB, prog.DB)
	}
	t.Logf("removed the site in %d runs; the saved progress reached database %d", runs, maxDB)
	if maxDB < 15 {
		t.Errorf("the progress only reached database %d: the runs did not cross databases", maxDB)
	}
	for db := 0; db < 16; db++ {
		if got := count(db, "s_shop:*"); got != 0 {
			t.Errorf("database %d still has %d keys of the site", db, got)
		}
	}
	for _, db := range []int{0, 1} {
		if got := count(db, "s_shop-x:*"); got != 2 {
			t.Errorf("database %d has %d keys of the neighbour, want 2", db, got)
		}
		if out := asUserDB("s_shop-x", passB1, db, "GET", "s_shop-x:a"); out != "keep" {
			t.Errorf("the neighbour's key in %d: %q", db, out)
		}
	}
	for _, db := range []int{0, 1, 2, 15} {
		if got := count(db, "other:bulk:*"); got != 30000 {
			t.Errorf("database %d has %d other keys, want 30000", db, got)
		}
	}
	if prog != (Progress{}) {
		t.Errorf("progress left: %v", prog)
	}
	// A site made again under the name finds nothing in any database.
	if err := ApplyUsers(ctx, d, st.users, st.setting("shop", passA2)); err != nil {
		t.Fatal(err)
	}
	for _, db := range []int{0, 1, 15} {
		if out := asUserDB("s_shop", passA2, db, "GET", "s_shop:a"); out != "" {
			t.Errorf("the new site reads %q in database %d", out, db)
		}
	}
}
