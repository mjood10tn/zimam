package commands_test

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"zimam/agent/contract"
	"zimam/agent/internal/commands"
	"zimam/agent/internal/jobs"
	"zimam/agent/internal/services"
	"zimam/agent/internal/sysexec"
	"zimam/agent/internal/valkey"
)

const (
	valkeyCli = "/usr/bin/valkey-cli"
	runA      = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	adminPass = "k9Qm2ZxL0vB7tR4nYc8WdE1aHs5UjP3fGoIq6XyTz_A"
)

func sum(pw contract.ServicePassword) string {
	h := sha256.Sum256([]byte(pw))
	return hex.EncodeToString(h[:])
}

// withValkey makes the server one where Valkey is installed: recorded, with
// its admin password and a users file that holds the sites' current users.
func withValkey(t *testing.T, e *dbEnv) *dbEnv {
	t.Helper()
	withLocks(t, e)
	if err := services.Update(e.p, func(s *services.State) { s.Valkey = services.Entry{Installed: true, Version: "9.0.4"} }); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{filepath.Dir(e.p.EtcGroup()), e.p.ValkeyDir()} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(e.p.EtcGroup(), fmt.Appendf(nil, "root:x:0:\nvalkey:x:%d:\n", os.Getgid()), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(e.p.ValkeyAdminPass(), []byte(adminPass+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	h := sha256.Sum256([]byte(adminPass))
	if err := os.WriteFile(e.p.ValkeyACL(), []byte("user default off\nuser zimam-admin on #"+hex.EncodeToString(h[:])+" ~* &* +@all\n"), 0o640); err != nil {
		t.Fatal(err)
	}
	e.run.Add(cliInfo)
	e.run.Add(cliDatabases)
	return e
}

// cli scripts one valkey-cli call as the admin to the server on loopback.
func cli(out string, code int, args ...string) sysexec.Script {
	argv := append([]string{"-e", "--no-auth-warning", "--user", "zimam-admin", "-h", "127.0.0.1", "-p", "6379"}, args...)
	return sysexec.Script{Path: valkeyCli, Args: argv, Result: sysexec.Result{Stdout: []byte(out), ExitCode: code}}
}

var (
	cliPing = cli("PONG\n", 0, "PING")
	cliLoad = cli("OK\n", 0, "ACL", "LOAD")
	// One database keeps the scripts of the purge short.
	cliDatabases = cli("databases\n1\n", 0, "CONFIG", "GET", "databases")
	// INFO server tells the process apart: a SCAN cursor is good only for one.
	cliInfo = cli("# Server\r\nvalkey_version:9.0.4\r\nrun_id:"+runA+"\r\n", 0, "INFO", "server")
)

func cliKill(site string, out string, code int) sysexec.Script {
	return cli(out, code, "CLIENT", "KILL", "USER", "s_"+site)
}

func cacheParams(site contract.SiteName, pw contract.ServicePassword) *contract.CacheUserParams {
	return &contract.CacheUserParams{Site: site, Password: pw}
}

func (e *dbEnv) acl(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(e.p.ValkeyACL())
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// valkeyRuns are the valkey-cli calls made, as the words after the connection
// options.
func (e *dbEnv) valkeyRuns() []string {
	var out []string
	for _, c := range e.run.Calls() {
		if c.Path == valkeyCli {
			out = append(out, strings.Join(valkeyWords(c), " "))
		}
	}
	return out
}

func aclLine(site string, pw contract.ServicePassword) string {
	return "user s_" + site + " on #" + sum(pw) + " resetkeys ~s_" + site + ":* resetchannels &s_" + site + ":* +@all -@admin -@dangerous -function -script|kill -script|flush -script|debug\n"
}

func TestCacheUserWired(t *testing.T) {
	d, ok := contract.Lookup("cache.user")
	if !ok || d.Kind != contract.KindJob || !d.Audited {
		t.Fatalf("cache.user = %+v, %v; want an audited job", d, ok)
	}
	if got := d.Subject(cacheParams("shop", pwOne)); got != "site:shop" {
		t.Errorf("subject %q", got)
	}
	if l, ok := jobs.LimitsFor("cache.user"); !ok || l != (jobs.Limits{MemoryMax: "256M", RuntimeMaxSec: 120}) {
		t.Errorf("limits %+v", l)
	}
	if commands.JobHandlers()["cache.user"] == nil || commands.Prechecks(sysexec.Deps{})["cache.user"] == nil {
		t.Error("cache.user has no handler or precheck")
	}
}

// A first creation stores the hash of the password and puts the site's user in
// the users file, next to the other sites' users; there is no connection to
// close.
func TestCacheUserCreated(t *testing.T) {
	e := withValkey(t, newDBEnv(t))
	blog := e.site(t, "blog")
	blog.CacheUserHash = sum(pwTwo)
	e.saveSite(t, blog)
	e.run.Add(cliPing)
	e.run.Add(cliLoad)
	res, err := e.job(t, "cache.user", cacheParams("shop", pwOne))
	if err != nil {
		t.Fatal(err)
	}
	if r, ok := res.(contract.ServiceUserResult); !ok || r.Site != "shop" {
		t.Errorf("result %+v", res)
	}
	if got := e.site(t, "shop").CacheUserHash; got != sum(pwOne) {
		t.Errorf("state hash = %q, want the SHA-256 of the password", got)
	}
	acl := e.acl(t)
	if !strings.Contains(acl, aclLine("shop", pwOne)) || !strings.Contains(acl, aclLine("blog", pwTwo)) {
		t.Errorf("users.acl =\n%s", acl)
	}
	if got := e.valkeyRuns(); !slices.Equal(got, []string{"PING", "ACL LOAD"}) {
		t.Errorf("valkey-cli ran %q; a first creation closes no connection", got)
	}
}

// After a rotation the agent closes the user's connections as the admin: a
// connection that was open keeps working with the old password otherwise.
func TestRotateKillsOldConnections(t *testing.T) {
	for name, again := range map[string]contract.ServicePassword{"a new password": pwTwo, "the same password": pwOne} {
		t.Run(name, func(t *testing.T) {
			e := withValkey(t, newDBEnv(t))
			st := e.site(t, "shop")
			st.CacheUserHash = sum(pwOne)
			e.saveSite(t, st)
			e.run.Add(cliPing)
			e.run.Add(cliLoad)
			e.run.Add(cliKill("shop", "2\n", 0))
			if _, err := e.job(t, "cache.user", cacheParams("shop", again)); err != nil {
				t.Fatal(err)
			}
			if got := e.valkeyRuns(); !slices.Equal(got, []string{"PING", "ACL LOAD", "CLIENT KILL USER s_shop"}) {
				t.Errorf("valkey-cli ran %q; the kill comes after the load", got)
			}
			if got := e.site(t, "shop").CacheUserHash; got != sum(again) {
				t.Errorf("state hash = %q", got)
			}
			if !strings.Contains(e.acl(t), aclLine("shop", again)) {
				t.Errorf("users.acl =\n%s", e.acl(t))
			}
		})
	}
}

// A kill that fails after the load leaves the new password in force. The
// error says so and that old connections may remain; running it again closes
// them.
func TestRotationWhoseKillFails(t *testing.T) {
	e := withValkey(t, newDBEnv(t))
	st := e.site(t, "shop")
	st.CacheUserHash = sum(pwOne)
	e.saveSite(t, st)
	e.run.Add(cliPing)
	e.run.Add(cliLoad)
	e.run.Add(cliKill("shop", "ERR server is busy\n", 1))
	_, err := e.job(t, "cache.user", cacheParams("shop", pwTwo))
	if err == nil {
		t.Fatal("a failed kill passed for success")
	}
	for _, w := range []string{"new password", "in force", "old password", "again"} {
		if !strings.Contains(err.Error(), w) {
			t.Errorf("the error %q doesn't say %q", err, w)
		}
	}
	if e.site(t, "shop").CacheUserHash != sum(pwTwo) || !strings.Contains(e.acl(t), aclLine("shop", pwTwo)) {
		t.Error("the new password is not in force in the state and the file")
	}
	// Again, with the same password: the connections are closed this time.
	e2 := cliKill("shop", "1\n", 0)
	e.run = sysexec.NewFakeRunner(cliPing, cliLoad, e2)
	e.d.Run = e.run
	if _, err := e.job(t, "cache.user", cacheParams("shop", pwTwo)); err != nil {
		t.Fatal(err)
	}
	if got := e.valkeyRuns(); !slices.Equal(got, []string{"PING", "ACL LOAD", "CLIENT KILL USER s_shop"}) {
		t.Errorf("the second run made %q", got)
	}
}

// A Valkey that refuses the file leaves the state, the file and the live users
// as they were, and no connection is closed.
func TestCacheUserWhoseLoadFails(t *testing.T) {
	e := withValkey(t, newDBEnv(t))
	st := e.site(t, "shop")
	st.CacheUserHash = sum(pwOne)
	e.saveSite(t, st)
	if err := os.WriteFile(e.p.ValkeyACL(), []byte(e.acl(t)+aclLine("shop", pwOne)), 0o640); err != nil {
		t.Fatal(err)
	}
	stateBefore, aclBefore := e.stateFile(t, "shop"), e.acl(t)
	e.run.Add(cliPing)
	e.run.Add(cli("ERR users.acl:3: Error in applying operation. WARNING: ACL errors detected, no change to the previously active ACL rules was performed\n", 1, "ACL", "LOAD"))
	e.run.Add(cliLoad)
	_, err := e.job(t, "cache.user", cacheParams("shop", pwTwo))
	if err == nil {
		t.Fatal("a refused file passed for success")
	}
	for _, w := range []string{"put back", "state"} {
		if !strings.Contains(err.Error(), w) {
			t.Errorf("the error %q doesn't say %q", err, w)
		}
	}
	if got := e.stateFile(t, "shop"); got != stateBefore {
		t.Errorf("state changed:\n%s\nwant\n%s", got, stateBefore)
	}
	if got := e.acl(t); got != aclBefore {
		t.Errorf("users.acl changed:\n%s", got)
	}
	if got := e.valkeyRuns(); !slices.Equal(got, []string{"PING", "ACL LOAD", "ACL LOAD"}) {
		t.Errorf("valkey-cli ran %q: the load, then the previous file's load, and no kill", got)
	}
}

// Without Valkey, or with it down, the answer is a conflict and nothing has
// changed.
func TestCacheUserNeedsAWorkingValkey(t *testing.T) {
	for name, setup := range map[string]func(*dbEnv){
		"not installed": func(e *dbEnv) {
			if err := services.Update(e.p, func(s *services.State) { s.Valkey = services.Entry{} }); err != nil {
				t.Fatal(err)
			}
		},
		"not running": func(e *dbEnv) {
			e.run.Add(cli("Could not connect to Valkey at 127.0.0.1:6379: Connection refused\n", 1, "PING"))
		},
	} {
		t.Run(name, func(t *testing.T) {
			e := withValkey(t, newDBEnv(t))
			setup(e)
			stateBefore, aclBefore := e.stateFile(t, "shop"), e.acl(t)
			_, err := e.job(t, "cache.user", cacheParams("shop", pwOne))
			wantCode(t, err, contract.ErrConflict)
			if name == "not installed" {
				wantCode(t, e.precheck(t, "cache.user", cacheParams("shop", pwOne)), contract.ErrConflict)
			}
			if e.stateFile(t, "shop") != stateBefore || e.acl(t) != aclBefore {
				t.Error("something changed")
			}
			if got := e.valkeyRuns(); len(got) > 1 {
				t.Errorf("valkey-cli ran %q", got)
			}
		})
	}
}

func TestCacheUserForAMissingSite(t *testing.T) {
	e := withValkey(t, newDBEnv(t))
	wantCode(t, e.precheck(t, "cache.user", cacheParams("nope", pwOne)), contract.ErrNotFound)
	_, err := e.job(t, "cache.user", cacheParams("nope", pwOne))
	wantCode(t, err, contract.ErrNotFound)
	if n := len(e.run.Calls()); n != 0 {
		t.Errorf("%d runs", n)
	}
}

// The password goes to the hash and nowhere else, whether the command
// worked, Valkey refused the file, or the kill failed.
func TestCachePasswordGoesNowhere(t *testing.T) {
	for name, scripts := range map[string][]sysexec.Script{
		"worked":       {cliPing, cliLoad, cliKill("shop", "1\n", 0)},
		"load fails":   {cliPing, cli("ERR bad file\n", 1, "ACL", "LOAD"), cliLoad},
		"kill fails":   {cliPing, cliLoad, cliKill("shop", "ERR down\n", 1)},
		"ping fails":   {cli("Could not connect\n", 1, "PING")},
		"load twice":   {cliPing, cli("ERR bad file\n", 1, "ACL", "LOAD"), cli("ERR bad again\n", 1, "ACL", "LOAD")},
		"no such user": {cliPing, cliLoad, cliKill("shop", "ERR No such user 's_shop'\n", 1)},
	} {
		t.Run(name, func(t *testing.T) {
			e := withValkey(t, newDBEnv(t))
			st := e.site(t, "shop")
			st.CacheUserHash = sum(pwOne)
			e.saveSite(t, st)
			for _, s := range scripts {
				e.run.Add(s)
			}
			var logs strings.Builder
			e.d.Logf = func(f string, a ...any) { fmt.Fprintf(&logs, f+"\n", a...) }
			h := commands.JobHandlers()["cache.user"]
			_, err := h(t.Context(), &jobs.Context{ID: "01JBX7Q3A1B2C3D4E5F6G7H8J9", Logf: e.d.Logf, Paths: e.p, Run: e.run}, cacheParams("shop", pwTwo))
			pw := string(pwTwo)
			var seen []string
			if err != nil {
				seen = append(seen, err.Error())
			}
			seen = append(seen, logs.String(), e.stateFile(t, "shop"), e.acl(t))
			for _, c := range e.run.Calls() {
				seen = append(seen, c.Path, strings.Join(c.Args, " "), c.Dir, string(c.Stdin), strings.Join(c.Env, " "))
			}
			for _, s := range seen {
				if strings.Contains(s, pw) {
					t.Errorf("the password turns up in %q", s)
				}
			}
			for _, c := range e.run.Calls() {
				if got, want := c.Env, []string{"REDISCLI_AUTH=" + adminPass}; !slices.Equal(got, want) {
					t.Errorf("%s ran with env %q", c.Path, got)
				}
			}
		})
	}
}

// valkey-cli is the only program the command runs, and only with fixed words
// and the site's name in the one user name.
func TestCacheUserRunsOnlyValkeyCli(t *testing.T) {
	e := withValkey(t, newDBEnv(t))
	e.run.Add(cliPing)
	e.run.Add(cliLoad)
	if _, err := e.job(t, "cache.user", cacheParams("shop-x", pwOne)); err != nil {
		t.Fatal(err)
	}
	for _, c := range e.run.Calls() {
		if c.Path != valkeyCli {
			t.Errorf("ran %s", c.Path)
		}
	}
}

// cliPurge scripts the first purge of a site's keys, then the second, as the
// admin: each a single page that ends the scan.
func cliPurge(site string, first, second int) []sysexec.Script {
	step := func(n int) sysexec.Script {
		return cli(fmt.Sprintf("0 %d\n", n), 0, valkey.PurgeArgs("0", contract.SiteName(site))...)
	}
	return []sysexec.Script{step(first), step(second)}
}

func (e *dbEnv) addPurge(site string) {
	for _, s := range cliPurge(site, 3, 1) {
		e.run.Add(s)
	}
}

// valkeyWords are the words of a valkey-cli call after its connection
// options, with the purge script shortened to PURGE and its pattern.
func valkeyWords(c sysexec.Call) []string {
	words := c.Args[8:]
	if words[0] == "EVAL" {
		return []string{"PURGE", words[4], "@" + words[3]}
	}
	return words
}
