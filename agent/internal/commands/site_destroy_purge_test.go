package commands_test

import (
	"slices"
	"strings"
	"testing"

	"zimam/agent/contract"
	"zimam/agent/internal/sites"
	"zimam/agent/internal/valkey"
)

// With MySQL down, a destroy that has databases to drop must not wipe the
// site's keys and user first: it fails before the first step of either.
func TestDestroyChecksMySQLBeforeTouchingValkey(t *testing.T) {
	e := withValkey(t, newDBEnv(t))
	st := e.site(t, "shop")
	st.CacheUserHash = sum(pwOne)
	st.Databases = []contract.DatabaseName{"s_shop_a"}
	st.DBUser = true
	e.saveSite(t, st)
	e.run.Add(cliPing)
	e.answer("", errDown)
	before, aclBefore := e.stateFile(t, "shop"), e.acl(t)
	_, err := e.job(t, "site.destroy", destroyParams(true))
	wantCode(t, err, contract.ErrConflict)
	if got := e.valkeyRuns(); !slices.Equal(got, []string{"PING"}) {
		t.Errorf("valkey-cli ran %q; only the liveness check may run", got)
	}
	if e.stateFile(t, "shop") != before || e.acl(t) != aclBefore {
		t.Error("something changed")
	}
}

// A purge that stops partway saves its cursor in the site's state, and the
// next destroy starts from it and finishes.
func TestDestroyResumesThePurgeFromTheState(t *testing.T) {
	e := withValkey(t, newDBEnv(t))
	st := e.site(t, "shop")
	st.CacheUserHash = sum(pwOne)
	e.saveSite(t, st)
	e.run.Add(cliPing)
	e.run.Add(cli("70 10\n", 0, valkey.PurgeArgs("0", "shop")...))
	e.run.Add(cli("ERR boom\n", 1, valkey.PurgeArgs("70", "shop")...))
	if _, err := e.job(t, "site.destroy", destroyParams(false)); err == nil {
		t.Fatal("the destroy passed")
	}
	got := e.site(t, "shop")
	if got.CacheUserHash == "" || got.CachePurge == nil || *got.CachePurge != (sites.CachePurge{Pass: 1, Cursor: "70", RunID: runA}) {
		t.Fatalf("state: hash %q, purge %+v; want the hash and pass 1 at cursor 70", got.CacheUserHash, got.CachePurge)
	}
	// Again: from 70, through the user's removal and the second pass.
	e.run.Add(cli("0 5\n", 0, valkey.PurgeArgs("70", "shop")...))
	e.run.Add(cliLoad)
	e.run.Add(cli("0 0\n", 0, valkey.PurgeArgs("0", "shop")...))
	if _, err := e.job(t, "site.destroy", destroyParams(false)); err != nil {
		t.Fatal(err)
	}
	if stateExists(e, "shop") {
		t.Error("the state file is left")
	}
}

// A site given a user afresh starts afresh: progress of an earlier removal is
// dropped with the old hash.
func TestCacheUserDropsPurgeProgress(t *testing.T) {
	e := withValkey(t, newDBEnv(t))
	st := e.site(t, "shop")
	st.CacheUserHash = sum(pwOne)
	st.CachePurge = &sites.CachePurge{Pass: 2, Cursor: "5"}
	e.saveSite(t, st)
	e.run.Add(cliPing)
	e.run.Add(cliLoad)
	e.run.Add(cliKill("shop", "0\n", 0))
	if _, err := e.job(t, "cache.user", cacheParams("shop", pwTwo)); err != nil {
		t.Fatal(err)
	}
	if got := e.site(t, "shop"); got.CachePurge != nil || got.CacheUserHash != sum(pwTwo) {
		t.Errorf("state: hash %q, purge %+v", got.CacheUserHash, got.CachePurge)
	}
}

// failedPassTwo is a server where shop's destroy got as far as its second
// pass and stopped: the user is out of the live users, the hash and the
// progress are in the state. blog has a user too.
func failedPassTwo(t *testing.T) *dbEnv {
	t.Helper()
	e := withValkey(t, newDBEnv(t))
	for n, pw := range map[contract.SiteName]contract.ServicePassword{"shop": pwOne, "blog": pwTwo} {
		st := e.site(t, n)
		st.CacheUserHash = sum(pw)
		e.saveSite(t, st)
	}
	e.run.Add(cliPing)
	e.run.Add(cli("0 3\n", 0, valkey.PurgeArgs("0", "shop")...))
	e.run.Add(cliLoad)
	e.run.Add(cli("ERR boom\n", 1, valkey.PurgeArgs("0", "shop")...))
	// (the first purge call above answers once, the second one fails)
	if _, err := e.job(t, "site.destroy", destroyParams(false)); err == nil {
		t.Fatal("the destroy passed")
	}
	got := e.site(t, "shop")
	if got.CachePurge == nil || got.CachePurge.Pass != 2 || got.CacheUserHash == "" {
		t.Fatalf("state: hash %q, purge %+v; want pass 2 in progress", got.CacheUserHash, got.CachePurge)
	}
	if strings.Contains(e.acl(t), "s_shop") {
		t.Fatalf("the live users still have s_shop:\n%s", e.acl(t))
	}
	return e
}

// A change to Valkey's users made by another command renders the users from
// the states; a site whose destroy has begun removing its keys is not
// brought back, or it would write keys behind the saved cursor.
func TestAnotherSitesChangeDoesNotBringBackASiteBeingDestroyed(t *testing.T) {
	e := failedPassTwo(t)
	e.run.Add(cliKill("blog", "0\n", 0))
	if _, err := e.job(t, "cache.user", cacheParams("blog", pwTwo)); err != nil {
		t.Fatal(err)
	}
	acl := e.acl(t)
	if strings.Contains(acl, "s_shop") || !strings.Contains(acl, aclLine("blog", pwTwo)) {
		t.Errorf("users.acl =\n%s", acl)
	}
	if e.site(t, "shop").CachePurge == nil {
		t.Error("the progress was dropped by another site's change")
	}
}

// cache.user on the site itself is the way to give up a destroy: it brings
// the user back with the new password and drops the progress.
func TestCacheUserOnASiteBeingDestroyedGivesItsUserBack(t *testing.T) {
	e := failedPassTwo(t)
	e.run.Add(cliKill("shop", "0\n", 0))
	if _, err := e.job(t, "cache.user", cacheParams("shop", pwTwo)); err != nil {
		t.Fatal(err)
	}
	got := e.site(t, "shop")
	if got.CachePurge != nil || got.CacheUserHash != sum(pwTwo) {
		t.Errorf("state: hash %q, purge %+v", got.CacheUserHash, got.CachePurge)
	}
	if !strings.Contains(e.acl(t), aclLine("shop", pwTwo)) || !strings.Contains(e.acl(t), aclLine("blog", pwTwo)) {
		t.Errorf("users.acl =\n%s", e.acl(t))
	}
}
