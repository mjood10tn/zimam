package sites

import (
	"context"
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

const (
	groupadd1001 = "/usr/sbin/groupadd -g 1001 s_shop"
	useradd1001  = "/usr/sbin/useradd -M -d /home/s_shop -s /usr/sbin/nologin -u 1001 -g 1001 s_shop"
)

// A group without its user is what a stopped provision or destroy leaves;
// its gid may be a deleted site's uid, so it is removed and a new id taken.
func TestProvisionRemovesALeftoverGroupAndTakesANewUID(t *testing.T) {
	// From a provision that stopped after groupadd.
	e := newEnv(t)
	e.appendGroup("s_shop:x:1000:\n")
	mkdir(t, e.p.StateDir())
	writeFile(t, e.p.UIDHighwater(), "{\"highest\": 1000}\n")
	e.run.Add(scriptOut(groupadd1001, ""))
	e.run.Add(scriptOut(useradd1001, ""))
	if _, err := e.provision(static); err != nil {
		t.Fatalf("Provision: %v", err)
	}
	checkFreshIDs(t, e)

	// From a destroy that stopped between userdel and groupdel.
	e = provisioned(t)
	delete(e.users, "s_shop")
	pw, _ := os.ReadFile(e.p.EtcPasswd())
	var kept []string
	for line := range strings.Lines(string(pw)) {
		if !strings.HasPrefix(line, "s_shop:") {
			kept = append(kept, line)
		}
	}
	writeFile(t, e.p.EtcPasswd(), strings.Join(kept, ""))
	if err := os.Remove(e.p.SiteState("shop")); err != nil {
		t.Fatal(err)
	}
	e.run.Add(scriptOut(groupadd1001, ""))
	e.run.Add(scriptOut(useradd1001, ""))
	n := len(e.run.Calls())
	e.fpmActive("8.4", false)
	if _, err := e.provision(php84); err != nil {
		t.Fatalf("Provision: %v", err)
	}
	checkFreshIDs(t, e, n)
}

func checkFreshIDs(t *testing.T, e *env, since ...int) {
	t.Helper()
	calls := e.calls()
	if len(since) > 0 {
		calls = calls[since[0]:]
	}
	g, a, u := slices.Index(calls, groupdelCmd), slices.Index(calls, groupadd1001), slices.Index(calls, useradd1001)
	if g < 0 || a < g || u < a {
		t.Errorf("want groupdel, groupadd -g 1001, useradd -u 1001 in that order:\n%s", strings.Join(calls, "\n"))
	}
	for _, c := range calls {
		if strings.Contains(c, "-u 1000") || strings.Contains(c, "-g 1000") {
			t.Errorf("the old id came back: %q", c)
		}
	}
}

// A deleted site's uid doesn't come back when the name is provisioned again.
func TestReprovisionedSiteGetsANewUID(t *testing.T) {
	e := provisioned(t)
	if _, err := e.destroy(); err != nil {
		t.Fatal(err)
	}
	e.run.Add(scriptOut(groupadd1001, ""))
	e.run.Add(scriptOut(useradd1001, ""))
	e.fpmActive("8.4", false)
	n := len(e.run.Calls())
	if _, err := e.provision(php84); err != nil {
		t.Fatalf("Provision: %v", err)
	}
	calls := e.callsSince(n)
	if !slices.Contains(calls, useradd1001) {
		t.Errorf("useradd -u 1001 -g 1001 didn't run:\n%s", strings.Join(calls, "\n"))
	}
	for _, c := range calls {
		if strings.Contains(c, "-u 1000") {
			t.Errorf("the old uid came back: %q", c)
		}
	}
}

// Two sites provisioned at once never get one uid: the lock is held from
// reading the mark to the end of useradd.
func TestConcurrentProvisionsGetDifferentUIDs(t *testing.T) {
	e := newEnv(t)
	for _, n := range []string{"s_aa", "s_bb"} {
		for _, id := range []string{"1000", "1001"} {
			e.run.Add(scriptOut("/usr/sbin/groupadd -g "+id+" "+n, ""))
			e.run.Add(scriptOut("/usr/sbin/useradd -M -d /home/"+n+" -s /usr/sbin/nologin -u "+id+" -g "+id+" "+n, ""))
		}
	}
	errs := make(chan error, 2)
	for _, n := range []string{"s_aa", "s_bb"} {
		go func() { errs <- createSiteUser(context.Background(), e.d, n) }()
	}
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatalf("createSiteUser: %v", err)
		}
	}
	pw, _ := os.ReadFile(e.p.EtcPasswd())
	seen := map[string]string{}
	for line := range strings.Lines(string(pw)) {
		f := strings.Split(line, ":")
		if strings.HasPrefix(f[0], "s_") {
			if other, dup := seen[f[2]]; dup {
				t.Fatalf("%s and %s share uid %s", other, f[0], f[2])
			}
			seen[f[2]] = f[0]
		}
	}
	if len(seen) != 2 {
		t.Fatalf("users = %v", seen)
	}
	if fi, err := os.Stat(e.p.UIDLock()); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("uid.lock: %v, %v; want 0600", fi, err)
	}
}

// v0.1.0's destroy left a deleted site's crontab and at jobs under its
// name. A new site of that name gets them removed right after useradd,
// before anything of it can run.
func TestProvisionRemovesSchedulesLeftUnderTheName(t *testing.T) {
	e := newEnv(t)
	install(t, e.p.Crontab(), e.p.Atq())
	e.run.Add(scriptFail(crontabCmd, 1, "no crontab for s_shop"))
	e.run.Add(scriptOut(atqCmd, "8\tFri Nov 14 10:00:00 2026 a s_shop\n9\tFri Nov 14 10:00:00 2026 a root\n"))
	e.run.Add(scriptOut("/usr/bin/atrm 8", ""))
	if _, err := e.provision(static); err != nil {
		t.Fatalf("Provision: %v", err)
	}
	calls := e.calls()
	u := slices.Index(calls, "/usr/sbin/useradd -M -d /home/s_shop -s /usr/sbin/nologin -u 1000 -g 1000 s_shop")
	m := slices.Index(calls, "/usr/sbin/usermod -aG s_shop www-data")
	c, q, r := slices.Index(calls, crontabCmd), slices.Index(calls, atqCmd), slices.Index(calls, "/usr/bin/atrm 8")
	if u < 0 || c < u || q < u || r < q || c > m || r > m {
		t.Errorf("crontab -r and atrm 8 must run right after useradd, before usermod:\n%s", strings.Join(calls, "\n"))
	}
}
