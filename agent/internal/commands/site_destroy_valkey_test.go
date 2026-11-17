package commands_test

import (
	"os"
	"slices"
	"strings"
	"testing"

	"zimam/agent/contract"
	"zimam/agent/internal/sites"
	"zimam/agent/internal/sysexec"
	"zimam/agent/internal/valkey"
)

// shopWithCache is a server where Valkey runs and shop and blog both have a
// Valkey user.
func shopWithCache(t *testing.T) *dbEnv {
	t.Helper()
	e := withValkey(t, newDBEnv(t))
	for name, pw := range map[contract.SiteName]contract.ServicePassword{"shop": pwOne, "blog": pwTwo} {
		st := e.site(t, name)
		st.CacheUserHash = sum(pw)
		e.saveSite(t, st)
	}
	if err := os.WriteFile(e.p.ValkeyACL(), []byte(e.acl(t)+aclLine("blog", pwTwo)+aclLine("shop", pwOne)), 0o640); err != nil {
		t.Fatal(err)
	}
	e.run.Add(cliPing)
	e.addPurge("shop")
	return e
}

func TestDestroyRemovesValkeyUser(t *testing.T) {
	e := shopWithCache(t)
	e.run.Add(cliLoad)
	if err := e.precheck(t, "site.destroy", destroyParams(false)); err != nil {
		t.Fatal(err)
	}
	if _, err := e.job(t, "site.destroy", destroyParams(false)); err != nil {
		t.Fatal(err)
	}
	acl := e.acl(t)
	if strings.Contains(acl, "s_shop") || !strings.Contains(acl, aclLine("blog", pwTwo)) {
		t.Errorf("users.acl =\n%s", acl)
	}
	// Removing a user closes its connections by itself; nothing is killed.
	if got := e.valkeyRuns(); !slices.Equal(got, []string{"PING", "INFO server", "PURGE s_shop:* @0", "ACL LOAD", "PURGE s_shop:* @0"}) {
		t.Errorf("valkey-cli ran %q", got)
	}
	if stateExists(e, "shop") {
		t.Error("the state file is left")
	}
}

// A site made again under the name of a destroyed one starts with no user:
// the users file has no line for it until cache.user runs.
func TestRecreatedSiteStartsWithoutACLUser(t *testing.T) {
	e := shopWithCache(t)
	e.run.Add(cliLoad)
	if _, err := e.job(t, "site.destroy", destroyParams(false)); err != nil {
		t.Fatal(err)
	}
	e.saveSite(t, newState("shop"))
	// Right after the destroy and the re-provision, before any other change to
	// Valkey's users renders the file again, the line is already gone.
	if strings.Contains(e.acl(t), "s_shop") || !strings.Contains(e.acl(t), aclLine("blog", pwTwo)) {
		t.Fatalf("users.acl after the destroy:\n%s", e.acl(t))
	}
	e.run.Add(cliKill("blog", "0\n", 0))
	if _, err := e.job(t, "cache.user", cacheParams("blog", pwTwo)); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(e.acl(t), "s_shop") {
		t.Errorf("the recreated site has a user:\n%s", e.acl(t))
	}
	if _, err := e.job(t, "cache.user", cacheParams("shop", pwOne)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(e.acl(t), aclLine("shop", pwOne)) {
		t.Errorf("cache.user did not give the site a user:\n%s", e.acl(t))
	}
}

// A site with no Valkey user never touches Valkey, on a server that has it and
// on one that hasn't.
func TestDestroyWithoutACacheUserLeavesValkeyAlone(t *testing.T) {
	for name, installed := range map[string]bool{"valkey installed": true, "no valkey": false} {
		t.Run(name, func(t *testing.T) {
			e := withValkey(t, newDBEnv(t))
			if !installed {
				removeServices(t, e)
			}
			before := e.acl(t)
			if err := e.precheck(t, "site.destroy", destroyParams(false)); err != nil {
				t.Fatal(err)
			}
			if _, err := e.job(t, "site.destroy", destroyParams(false)); err != nil {
				t.Fatal(err)
			}
			if got := e.valkeyRuns(); len(got) != 0 {
				t.Errorf("valkey-cli ran %q", got)
			}
			if e.acl(t) != before {
				t.Error("the users file changed")
			}
			if stateExists(e, "shop") {
				t.Error("the state file is left")
			}
		})
	}
}

// A user that has to be removed needs a Valkey that is installed and answers,
// or nothing changes, including MySQL.
func TestDestroyNeedsValkeyForASiteWithAUser(t *testing.T) {
	t.Run("not installed", func(t *testing.T) {
		e := shopWithCache(t)
		st := e.site(t, "shop")
		st.Databases = []contract.DatabaseName{"s_shop_a"}
		e.saveSite(t, st)
		removeServices(t, e)
		before := e.stateFile(t, "shop")
		for _, err := range []error{e.precheck(t, "site.destroy", destroyParams(true)), mustFail(e.job(t, "site.destroy", destroyParams(true)))} {
			wantCode(t, err, contract.ErrConflict)
		}
		if n := len(e.run.Calls()); n != 0 || e.stateFile(t, "shop") != before {
			t.Errorf("%d runs, state changed: %v", n, e.stateFile(t, "shop") != before)
		}
	})
	t.Run("not running", func(t *testing.T) {
		e := withValkey(t, newDBEnv(t))
		st := e.site(t, "shop")
		st.CacheUserHash = sum(pwOne)
		st.Databases = []contract.DatabaseName{"s_shop_a"}
		st.DBUser = true
		e.saveSite(t, st)
		e.run.Add(cli("Could not connect to Valkey at 127.0.0.1:6379: Connection refused\n", 1, "PING"))
		before := e.stateFile(t, "shop")
		_, err := e.job(t, "site.destroy", destroyParams(true))
		wantCode(t, err, contract.ErrConflict)
		for _, c := range e.run.Calls() {
			if c.Path == mysqlClient {
				t.Error("MySQL was touched before Valkey was found down")
			}
		}
		if e.stateFile(t, "shop") != before {
			t.Error("state changed")
		}
	})
}

// If Valkey refuses the file the hash is back in the state and the site is
// whole, so the destroy can be run again.
func TestDestroyWhoseValkeyLoadFailsLeavesAWorkingSite(t *testing.T) {
	e := shopWithCache(t)
	before := e.stateFile(t, "shop")
	aclBefore := e.acl(t)
	e.run.Add(cli("ERR bad file\n", 1, "ACL", "LOAD"))
	e.run.Add(cliLoad)
	if _, err := e.job(t, "site.destroy", destroyParams(false)); err == nil {
		t.Fatal("the destroy passed")
	}
	if !stateExists(e, "shop") || e.stateFile(t, "shop") != before || e.acl(t) != aclBefore {
		t.Error("the site is not as it was")
	}
	// Run again; Valkey is well this time.
	e.run.Add(cliPing)
	if _, err := e.job(t, "site.destroy", destroyParams(false)); err != nil {
		t.Fatal(err)
	}
	if stateExists(e, "shop") || strings.Contains(e.acl(t), "s_shop") {
		t.Error("the second destroy didn't finish")
	}
}

// Valkey goes before MySQL, and a later MySQL failure doesn't bring the hash
// back from a copy of the state held in memory.
func TestDestroyValkeyThenMySQL(t *testing.T) {
	e := withValkey(t, newDBEnv(t))
	st := e.site(t, "shop")
	st.CacheUserHash = sum(pwOne)
	st.Databases = []contract.DatabaseName{"s_shop_a", "s_shop_b"}
	st.DBUser = true
	e.saveSite(t, st)
	e.run.Add(cliPing)
	e.addPurge("shop")
	e.run.Add(cliLoad)
	e.answer("", "") // MySQL answers a ping first
	e.answer("", "")
	e.answer("", "")
	e.answer("", "ERROR 1396 (HY000) at line 1: Operation failed")
	if _, err := e.job(t, "site.destroy", destroyParams(true)); err == nil {
		t.Fatal("the destroy passed")
	}
	var order []string
	for _, c := range e.run.Calls() {
		if c.Path == valkeyCli {
			order = append(order, "valkey "+strings.Join(valkeyWords(c), " "))
		} else {
			order = append(order, "mysql")
		}
	}
	if want := []string{"valkey PING", "mysql", "valkey INFO server", "valkey PURGE s_shop:* @0", "valkey ACL LOAD", "valkey PURGE s_shop:* @0", "mysql", "mysql", "mysql"}; !slices.Equal(order, want) {
		t.Errorf("order %q, want %q", order, want)
	}
	got := e.site(t, "shop")
	if got.CacheUserHash != "" {
		t.Error("a MySQL step saved the old hash back")
	}
	if len(got.Databases) != 1 || got.Databases[0] != "s_shop_b" {
		t.Errorf("databases %v, want only s_shop_b left", got.Databases)
	}
}

func newState(name contract.SiteName) sites.State {
	return sites.State{Name: name, Domain: contract.Domain(string(name) + ".example.com"), Type: contract.SiteStatic, TemplateVersion: 1, Security: sites.DefaultSecurity()}
}

// The hash leaves the state only after the last purge, and the step that
// failed is repeated by running the destroy again, which ends the same way.
func TestDestroyFailureAtEachValkeyStepConverges(t *testing.T) {
	purgeFail := cli("ERR boom\n", 1, valkey.PurgeArgs("0", "shop")...)
	for name, tc := range map[string]struct {
		scripts      []sysexec.Script
		wantRuns     []string
		aclHasShop   bool
		hashRemained bool
	}{
		"the first purge fails": {
			scripts:      []sysexec.Script{purgeFail},
			wantRuns:     []string{"PING", "INFO server", "PURGE s_shop:* @0"},
			aclHasShop:   true,
			hashRemained: true,
		},
		"the load fails": {
			scripts:      append(cliPurge("shop", 3, 0), cli("ERR bad\n", 1, "ACL", "LOAD"), cliLoad),
			wantRuns:     []string{"PING", "INFO server", "PURGE s_shop:* @0", "ACL LOAD", "ACL LOAD"},
			aclHasShop:   true,
			hashRemained: true,
		},
		"the second purge fails": {
			scripts:      []sysexec.Script{cli("0 3\n", 0, valkey.PurgeArgs("0", "shop")...), cliLoad, purgeFail},
			wantRuns:     []string{"PING", "INFO server", "PURGE s_shop:* @0", "ACL LOAD", "PURGE s_shop:* @0"},
			aclHasShop:   false, // the live users lack it; the state still has the hash
			hashRemained: true,
		},
	} {
		t.Run(name, func(t *testing.T) {
			e := withValkey(t, newDBEnv(t))
			for n, pw := range map[contract.SiteName]contract.ServicePassword{"shop": pwOne, "blog": pwTwo} {
				st := e.site(t, n)
				st.CacheUserHash = sum(pw)
				e.saveSite(t, st)
			}
			if err := os.WriteFile(e.p.ValkeyACL(), []byte(e.acl(t)+aclLine("blog", pwTwo)+aclLine("shop", pwOne)), 0o640); err != nil {
				t.Fatal(err)
			}
			e.run.Add(cliPing)
			for _, s := range tc.scripts {
				e.run.Add(s)
			}
			if _, err := e.job(t, "site.destroy", destroyParams(false)); err == nil {
				t.Fatal("the destroy passed")
			}
			if got := e.valkeyRuns(); !slices.Equal(got, tc.wantRuns) {
				t.Errorf("valkey-cli ran %q, want %q", got, tc.wantRuns)
			}
			if !stateExists(e, "shop") || (e.site(t, "shop").CacheUserHash != "") != tc.hashRemained {
				t.Errorf("state: exists %v, hash %q; the hash stays until the last purge", stateExists(e, "shop"), e.site(t, "shop").CacheUserHash)
			}
			if has := strings.Contains(e.acl(t), "s_shop"); has != tc.aclHasShop {
				t.Errorf("users.acl has s_shop = %v, want %v", has, tc.aclHasShop)
			}
			// Again, Valkey well: the same steps run and the destroy ends.
			e.run = sysexec.NewFakeRunner(append([]sysexec.Script{cliPing, cliInfo, cliLoad}, cliPurge("shop", 0, 0)...)...)
			e.d.Run = e.run
			if _, err := e.job(t, "site.destroy", destroyParams(false)); err != nil {
				t.Fatal(err)
			}
			want := []string{"PING", "INFO server", "PURGE s_shop:* @0", "ACL LOAD", "PURGE s_shop:* @0"}
			if name == "the second purge fails" {
				// The first pass and the user's removal are done and recorded, so the
				// second run resumes the second pass (after loading the users again).
				want = []string{"PING", "INFO server", "ACL LOAD", "PURGE s_shop:* @0"}
			}
			if got := e.valkeyRuns(); !slices.Equal(got, want) {
				t.Errorf("the second run made %q, want %q", got, want)
			}
			if stateExists(e, "shop") || strings.Contains(e.acl(t), "s_shop") || !strings.Contains(e.acl(t), aclLine("blog", pwTwo)) {
				t.Errorf("the second destroy didn't end: state %v, users.acl\n%s", stateExists(e, "shop"), e.acl(t))
			}
		})
	}
}
