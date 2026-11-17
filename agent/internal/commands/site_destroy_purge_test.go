package commands_test

import (
	"slices"
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
	if got.CacheUserHash == "" || got.CachePurge == nil || *got.CachePurge != (sites.CachePurge{Pass: 1, Cursor: "70"}) {
		t.Fatalf("state: hash %q, purge %+v; want the hash and pass 1 at cursor 70", got.CacheUserHash, got.CachePurge)
	}
	// Again: from 70, through the user's removal and the second pass.
	e.run.Add(cli("0 5\n", 0, valkey.PurgeArgs("70", "shop")...))
	e.run.Add(cliLoad)
	e.run.Add(cli("0 0\n", 0, valkey.PurgeArgs("0", "shop")...))
	e.run.Add(cli("0 0\n", 0, valkey.PurgeArgs("70", "shop")...)) // (not reached: same args, other pass)
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
