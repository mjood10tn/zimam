package sites

import (
	"os"
	"slices"
	"strings"
	"testing"
)

const (
	groupadd1006 = "/usr/sbin/groupadd -g 1006 s_shop"
	useradd1006  = "/usr/sbin/useradd -M -d /home/s_shop -s /usr/sbin/nologin -u 1006 -g 1006 s_shop"
)

// A uid is chosen above everything ever used, and groupadd and useradd both
// get it explicitly.
func TestProvisionPassesExplicitUID(t *testing.T) {
	e := newEnv(t)
	e.appendPasswd("s_old:x:1003:1003::/home/s_old:/usr/sbin/nologin\n")
	mkdir(t, e.p.StateDir())
	writeFile(t, e.p.UIDHighwater(), "{\"highest\": 1005}\n")
	e.run.Add(scriptOut(groupadd1006, ""))
	e.run.Add(scriptOut(useradd1006, ""))
	if _, err := e.provision(static); err != nil {
		t.Fatalf("Provision: %v", err)
	}
	calls := e.calls()
	g, u := slices.Index(calls, groupadd1006), slices.Index(calls, useradd1006)
	if g < 0 || u < 0 || g > u {
		t.Errorf("groupadd -g 1006 must run first, then useradd -u 1006 -g 1006:\n%s", strings.Join(calls, "\n"))
	}
	b, err := os.ReadFile(e.p.UIDHighwater())
	if err != nil || !strings.Contains(string(b), "1006") {
		t.Errorf("mark = %q, %v", b, err)
	}
	if fi, _ := os.Stat(e.p.UIDHighwater()); fi == nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("mark file is %v, want 0600", fi)
	}
}

// A retry of a site whose user exists makes no user and moves no mark.
func TestProvisionRetryKeepsExistingUser(t *testing.T) {
	e := newEnv(t)
	if _, err := e.provision(static); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(e.p.UIDHighwater())
	n := len(e.run.Calls())
	if err := os.Remove(e.p.SiteState("shop")); err != nil {
		t.Fatal(err)
	}
	if _, err := e.provision(static); err != nil {
		t.Fatalf("the retry: %v", err)
	}
	for _, c := range e.callsSince(n) {
		if strings.Contains(c, "useradd") || strings.Contains(c, "groupadd") {
			t.Errorf("the retry ran %q", c)
		}
	}
	if after, _ := os.ReadFile(e.p.UIDHighwater()); string(after) != string(before) {
		t.Errorf("the mark moved: %q -> %q", before, after)
	}
}

// groupadd ran but useradd didn't: the retry reuses that group's id.
func TestProvisionRetryAfterGroupaddReusesTheGroup(t *testing.T) {
	e := newEnv(t)
	e.appendGroup("s_shop:x:1000:\n")
	mkdir(t, e.p.StateDir())
	writeFile(t, e.p.UIDHighwater(), "{\"highest\": 1000}\n")
	if _, err := e.provision(static); err != nil {
		t.Fatalf("Provision: %v", err)
	}
	calls := e.calls()
	if slices.Contains(calls, groupaddCmd) || !slices.Contains(calls, useraddCmd) {
		t.Errorf("ran\n%s", strings.Join(calls, "\n"))
	}
}

// A deleted site's uid doesn't come back when the name is provisioned again.
func TestReprovisionedSiteGetsANewUID(t *testing.T) {
	e := provisioned(t)
	if _, err := e.destroy(); err != nil {
		t.Fatal(err)
	}
	e.run.Add(scriptOut("/usr/sbin/groupadd -g 1001 s_shop", ""))
	e.run.Add(scriptOut("/usr/sbin/useradd -M -d /home/s_shop -s /usr/sbin/nologin -u 1001 -g 1001 s_shop", ""))
	e.fpmActive("8.4", false)
	if _, err := e.provision(php84); err != nil {
		t.Fatalf("Provision: %v", err)
	}
}
