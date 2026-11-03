package sites

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"zimam/agent/contract"
)

const (
	pkillCmd    = "/usr/bin/pkill -KILL -u s_shop"
	pgrepCmd    = "/usr/bin/pgrep -u s_shop"
	userdelCmd  = "/usr/sbin/userdel s_shop"
	groupdelCmd = "/usr/sbin/groupdel s_shop"
	certDelete  = "/usr/bin/certbot delete --cert-name zimam-shop --non-interactive"
)

func (e *env) destroy() (contract.SiteNameResult, error) {
	return Destroy(context.Background(), e.d, e.help, e.account, "shop")
}

// provisioned is a server with the PHP site shop provisioned on 8.4, its
// FPM service running.
func provisioned(t *testing.T) *env {
	t.Helper()
	e := newEnv(t)
	e.fpmActive("8.4", false)
	if _, err := e.provision(php84); err != nil {
		t.Fatalf("provisioning shop: %v", err)
	}
	return e
}

// Nothing of the site is left: not its Nginx files, pool, processes, home,
// logs, log rules, user, group or state file (§9.8).
func TestDestroySequence(t *testing.T) {
	e := provisioned(t)
	n := len(e.run.Calls())
	res, err := e.destroy()
	if err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	if res != (contract.SiteNameResult{Name: "shop"}) {
		t.Errorf("result = %+v", res)
	}
	want := []string{nginxTest, nginxReload, fpmDisable84, pkillCmd, pgrepCmd, userdelCmd, groupdelCmd}
	if got := e.callsSince(n); !slices.Equal(got, want) {
		t.Errorf("ran\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	for _, path := range []string{
		e.p.NginxSiteEnabled("shop"), e.p.NginxSiteAvailable("shop"), e.p.SiteNginxDir("shop"), e.p.PHPPool("8.4", "shop"),
		e.p.SiteHome("shop"), e.p.SiteLogDir("shop"), e.p.SiteLogrotateConf("shop"), e.p.SiteState("shop"),
	} {
		if exists(path) {
			t.Errorf("%s is left", path)
		}
	}
	if e.users["s_shop"] {
		t.Error("the user is left")
	}
	if group, _ := os.ReadFile(e.p.EtcGroup()); strings.Contains(string(group), "s_shop") {
		t.Errorf("the group is left:\n%s", group)
	}
}

// Root never deletes inside the home: the helper empties it as the site
// user, so a symlink planted there can't steer the delete (§9.8 step 4).
func TestDestroyUsesHelperForHome(t *testing.T) {
	e := provisioned(t)
	ops := len(e.help.ops)
	if _, err := e.destroy(); err != nil {
		t.Fatal(err)
	}
	var changes []string
	for _, op := range e.help.ops[ops:] {
		if !strings.Contains(op, " stat ") {
			changes = append(changes, op)
		}
	}
	if want := []string{helperUser + " remove-tree ."}; !slices.Equal(changes, want) {
		t.Errorf("helper ran %q, want %q", changes, want)
	}
	if len(e.help.nodes) != 1 {
		t.Errorf("the helper left %d entries in the home", len(e.help.nodes)-1)
	}
}

// Anything the helper couldn't remove fails the job before the user goes,
// so a rerun can still act as that user.
func TestDestroyFailsWhenHomeNotEmpty(t *testing.T) {
	e := provisioned(t)
	writeFile(t, filepath.Join(e.p.SiteHome("shop"), "left"), "")
	_, err := e.destroy()
	if err == nil || !strings.Contains(err.Error(), e.p.SiteHome("shop")) {
		t.Fatalf("Destroy = %v, want the home named", err)
	}
	if slices.Contains(e.calls(), userdelCmd) || !e.users["s_shop"] {
		t.Error("the user was deleted with its home not empty")
	}
	if !exists(e.p.SiteState("shop")) {
		t.Error("the state file went before the job finished")
	}
}

// pkill only signals: the job waits until pgrep finds nothing, and fails if
// processes outlive the wait.
func TestDestroyWaitsForProcesses(t *testing.T) {
	e := provisioned(t)
	e.procs = 2 // pgrep finds them twice
	n := len(e.run.Calls())
	if _, err := e.destroy(); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	if got := e.callsSince(n); count(got, pgrepCmd) != 3 {
		t.Errorf("pgrep ran %d times, want 3: %q", count(got, pgrepCmd), got)
	}

	defer func(w time.Duration) { processWait = w }(processWait)
	processWait = 300 * time.Millisecond
	e = provisioned(t)
	e.procs = -1 // for ever
	_, err := e.destroy()
	if err == nil || !strings.Contains(err.Error(), "s_shop") {
		t.Fatalf("Destroy = %v, want the processes' user named", err)
	}
	if slices.Contains(e.calls(), userdelCmd) || len(e.help.nodes) == 1 {
		t.Error("the home or user went while the user's processes still ran")
	}
}

// certbot delete runs only when the site has a certificate, and the expiry
// record goes with it.
func TestDestroyWithoutCertificate(t *testing.T) {
	e := provisioned(t)
	if _, err := e.destroy(); err != nil {
		t.Fatal(err)
	}
	if slices.Contains(e.calls(), certDelete) {
		t.Error("certbot delete ran for a site without a certificate")
	}

	e = provisioned(t)
	mkdir(t, filepath.Dir(e.p.LetsEncryptCert("zimam-shop")))
	writeFile(t, e.p.LetsEncryptCert("zimam-shop"), "")
	mkdir(t, e.p.CertsDir())
	writeFile(t, e.p.CertRecord("shop"), "{}")
	if _, err := e.destroy(); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(e.calls(), certDelete) {
		t.Error("certbot delete didn't run for a site with a certificate")
	}
	if exists(e.p.CertRecord("shop")) {
		t.Error("the certificate's expiry record is left")
	}
}

// A destroy that failed in step 5 is finished by running it again: what
// is already gone is skipped. The state file still names PHP 8.4, so its
// FPM is brought in line again, a no-op stop on a real server.
func TestDestroyIsRerunnable(t *testing.T) {
	e := provisioned(t)
	e.failOnce[userdelCmd] = "userdel: user s_shop is currently used by process 4242"
	if _, err := e.destroy(); err == nil {
		t.Fatal("Destroy passed with userdel failing")
	}
	n := len(e.run.Calls())
	if _, err := e.destroy(); err != nil {
		t.Fatalf("the rerun: %v", err)
	}
	want := []string{fpmDisable84, pkillCmd, pgrepCmd, userdelCmd, groupdelCmd}
	if got := e.callsSince(n); !slices.Equal(got, want) {
		t.Errorf("the rerun ran\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if exists(e.p.SiteState("shop")) || e.users["s_shop"] {
		t.Error("the rerun left the state file or the user")
	}
}

// A site whose provisioning failed has no state file yet, but can still be
// destroyed; only a site with nothing on the server is not_found.
func TestDestroyCheck(t *testing.T) {
	e := newEnv(t)
	wantCode(t, CheckDestroy(e.d, e.account, "shop"), contract.ErrNotFound)

	failNginx(e)
	if _, err := e.provision(static); err == nil {
		t.Fatal("Provision passed with nginx -t failing")
	}
	if err := CheckDestroy(e.d, e.account, "shop"); err != nil {
		t.Errorf("a half-provisioned site: %v", err)
	}
	if _, err := e.destroy(); err != nil {
		t.Fatalf("destroying it: %v", err)
	}
	wantCode(t, CheckDestroy(e.d, e.account, "shop"), contract.ErrNotFound)
}

func count(s []string, v string) int {
	n := 0
	for _, x := range s {
		if x == v {
			n++
		}
	}
	return n
}
