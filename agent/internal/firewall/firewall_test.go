package firewall

import (
	"context"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"zimam/agent/contract"
	"zimam/agent/internal/paths"
	"zimam/agent/internal/sysexec"
)

// tupleLine writes a user.rules tuple as UFW does.
func tupleLine(action, proto, port, src, comment string) string {
	dst := "0.0.0.0/0"
	if strings.Contains(src, ":") {
		dst = "::/0"
	}
	line := "### tuple ### " + action + " " + proto + " " + port + " " + dst + " any " + src + " in"
	if comment != "" {
		line += " comment=" + hex.EncodeToString([]byte(comment))
	}
	return line + "\n-A ufw-user-input -p " + proto + " --dport " + port + " -j ACCEPT\n"
}

// installerRules are installer step 6's rules, untagged, in both families.
func installerRules() (v4, v6 string) {
	for _, port := range []string{"22", "80", "443"} {
		v4 += tupleLine("allow", "tcp", port, "0.0.0.0/0", "")
		v6 += tupleLine("allow", "tcp", port, "::/0", "")
	}
	return v4, v6
}

type env struct {
	d   sysexec.Deps
	run *sysexec.FakeRunner
}

func newEnv(t *testing.T, v4, v6 string, scripts ...sysexec.Script) env {
	t.Helper()
	p := paths.Paths{Root: t.TempDir()}
	for _, dir := range []string{filepath.Dir(p.UFWUserRules()), filepath.Dir(p.UFWDefaults()), p.LocksDir()} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write := func(path, s string, mode os.FileMode) {
		if err := os.WriteFile(path, []byte(s), mode); err != nil {
			t.Fatal(err)
		}
	}
	write(p.UFWUserRules(), "*filter\n### RULES ###\n"+v4+"### END RULES ###\n", 0o640)
	write(p.UFWUser6Rules(), "*filter\n### RULES ###\n"+v6+"### END RULES ###\n", 0o640)
	write(p.UFWConf(), "ENABLED=yes\nLOGLEVEL=low\n", 0o644)
	write(p.UFWDefaults(), "IPV6=yes\n", 0o644)
	run := sysexec.NewFakeRunner(scripts...)
	return env{d: sysexec.Deps{Paths: p, Run: run, Logf: t.Logf, Now: func() time.Time { return time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC) }}, run: run}
}

func sshdT(ports ...string) sysexec.Script {
	out := "addressfamily any\n"
	for _, p := range ports {
		out += "port " + p + "\n"
	}
	return sysexec.Script{Path: sshd, Args: []string{"-T"}, Result: sysexec.Result{Stdout: []byte(out + "permitrootlogin prohibit-password\n")}}
}

func ufwScript(args ...string) sysexec.Script { return sysexec.Script{Path: ufw, Args: args} }

func appRule() contract.FirewallRule {
	return contract.FirewallRule{ID: "app", Action: "allow", Proto: "tcp", PortFrom: 8080, PortTo: 8080, From: contract.IPList{"203.0.113.0/24", "2001:db8::/32"}}
}

func TestPlanAddsAndDeletesOnlyTaggedRules(t *testing.T) {
	v4, v6 := installerRules()
	v4 += tupleLine("allow", "tcp", "9000", "0.0.0.0/0", "zimam:old")
	v6 += tupleLine("allow", "tcp", "9000", "::/0", "zimam:old")
	v4 += tupleLine("allow", "udp", "51820", "0.0.0.0/0", "wireguard") // the owner's own
	tuples := append(parseTuples([]byte(v4), false), parseTuples([]byte(v6), true)...)
	got, err := plan(append([]contract.FirewallRule{appRule()}, lockedRules([]contract.TCPPort{22}, tuples)...), tuples, true)
	if err != nil {
		t.Fatal(err)
	}
	want := [][]string{
		{"delete", "allow", "proto", "tcp", "from", "any", "to", "any", "port", "9000"},
		{"allow", "proto", "tcp", "from", "203.0.113.0/24", "to", "any", "port", "8080", "comment", "zimam:app"},
		{"allow", "proto", "tcp", "from", "2001:db8::/32", "to", "any", "port", "8080", "comment", "zimam:app"},
		// The installer's rules are tagged in place.
		{"allow", "proto", "tcp", "from", "any", "to", "any", "port", "22", "comment", "zimam:ssh-22"},
		{"allow", "proto", "tcp", "from", "any", "to", "any", "port", "80", "comment", "zimam:http"},
		{"allow", "proto", "tcp", "from", "any", "to", "any", "port", "443", "comment", "zimam:https"},
	}
	if !slices.EqualFunc(got, want, slices.Equal) {
		t.Errorf("plan =\n%q\nwant\n%q", got, want)
	}
}

func TestPlanIsEmptyWhenRulesMatch(t *testing.T) {
	v4 := tupleLine("allow", "tcp", "22", "0.0.0.0/0", "zimam:ssh-22") + tupleLine("allow", "tcp", "8080", "203.0.113.0/24", "zimam:app")
	v6 := tupleLine("allow", "tcp", "22", "::/0", "zimam:ssh-22") + tupleLine("allow", "tcp", "8080", "2001:db8::/32", "zimam:app")
	v4 += tupleLine("allow", "tcp", "80", "0.0.0.0/0", "zimam:http") + tupleLine("allow", "tcp", "443", "0.0.0.0/0", "zimam:https")
	tuples := append(parseTuples([]byte(v4), false), parseTuples([]byte(v6), true)...)
	got, err := plan(append([]contract.FirewallRule{appRule()}, lockedRules([]contract.TCPPort{22}, tuples)...), tuples, false)
	if err != nil || len(got) != 0 {
		t.Errorf("plan = %q, %v; want nothing to do", got, err)
	}
}

func TestDenyIsPrepended(t *testing.T) {
	r := contract.FirewallRule{ID: "block", Action: "deny", Proto: "tcp", PortFrom: 22, PortTo: 22, From: contract.IPList{"192.0.2.9", "2001:db8::9"}}
	got, err := plan([]contract.FirewallRule{r}, nil, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range got {
		if args[0] != "prepend" || args[5] == "192.0.2.9/32" {
			t.Errorf("deny args %q: want prepend and a bare address", args)
		}
	}
}

func TestLockedRulesAlwaysPresent(t *testing.T) {
	got := lockedRules([]contract.TCPPort{22, 2222}, nil)
	var ids []contract.FirewallRuleID
	for _, r := range got {
		ids = append(ids, r.ID)
	}
	if !slices.Equal(ids, []contract.FirewallRuleID{"ssh-22", "ssh-2222", "http", "https"}) {
		t.Errorf("locked = %v", ids)
	}
	// An http rule already there (Cloudflare mode narrows it) is left alone.
	cf := parseTuples([]byte(tupleLine("allow", "tcp", "80", "173.245.48.0/20", "zimam:http")), false)
	for _, r := range lockedRules([]contract.TCPPort{22}, cf) {
		if r.ID == "http" {
			t.Error("lockedRules replaces an existing http rule")
		}
	}
}

func TestRemovingSSHRuleRefused(t *testing.T) {
	// ssh-2222 is from a port sshd no longer uses: only the SSH change's
	// confirmation removes it, never firewall.rules.
	v4 := tupleLine("allow", "tcp", "2222", "0.0.0.0/0", "zimam:ssh-2222")
	tuples := parseTuples([]byte(v4), false)
	got, err := plan(lockedRules([]contract.TCPPort{22}, tuples), tuples, false)
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range got {
		if args[0] == "delete" {
			t.Errorf("plan deletes a locked rule: %q", args)
		}
	}
}

func TestExternalRulesUntouched(t *testing.T) {
	v4 := tupleLine("deny", "tcp", "8080", "203.0.113.0/24", "")
	tuples := parseTuples([]byte(v4), false)
	_, err := plan([]contract.FirewallRule{appRule()}, tuples, false)
	var ae *contract.AgentError
	if !errors.As(err, &ae) || ae.Code != contract.ErrConflict {
		t.Errorf("plan over an external rule with another action = %v, want conflict", err)
	}
	other := parseTuples([]byte(tupleLine("allow", "udp", "53", "0.0.0.0/0", "")), false)
	got, err := plan(nil, other, false)
	if err != nil || len(got) != 0 {
		t.Errorf("plan touched an external rule: %q, %v", got, err)
	}
}

func TestRulesApplyToBothFamilies(t *testing.T) {
	// The v4 rule exists, the v6 one doesn't: "from any" adds it (UFW skips
	// the v4 one).
	tuples := parseTuples([]byte(tupleLine("allow", "tcp", "22", "0.0.0.0/0", "zimam:ssh-22")), false)
	ssh := lockedRules([]contract.TCPPort{22}, tuples)[:1]
	got, _ := plan(ssh, tuples, true)
	if len(got) != 1 || got[0][4] != "any" {
		t.Errorf("with IPv6 on: plan = %q, want one from-any add", got)
	}
	if got, _ := plan(ssh, tuples, false); len(got) != 0 {
		t.Errorf("with IPv6 off: plan = %q, want nothing", got)
	}
}

func TestDenyFromAnywhereOnLockedPortRefused(t *testing.T) {
	e := newEnv(t, "", "")
	e.run.Add(sshdT("22", "2222"))
	anywhere := func(action contract.FirewallAction, proto contract.FirewallProto, from, to contract.TCPPort, src ...string) contract.FirewallRule {
		return contract.FirewallRule{ID: "r", Action: action, Proto: proto, PortFrom: from, PortTo: to, From: contract.IPList(append([]string{}, src...))}
	}
	for name, tc := range map[string]struct {
		rule contract.FirewallRule
		ok   bool
	}{
		"deny range over 2222": {anywhere("deny", "tcp", 2000, 3000), false},
		"limit 443":            {anywhere("limit", "tcp", 443, 443), false},
		"deny 80":              {anywhere("deny", "tcp", 80, 80), false},
		"deny one address":     {anywhere("deny", "tcp", 22, 22, "192.0.2.9"), true},
		"allow 22":             {anywhere("allow", "tcp", 22, 22), true},
		"deny udp 443":         {anywhere("deny", "udp", 443, 443), true},
		"deny 3306":            {anywhere("deny", "tcp", 3306, 3306), true},
	} {
		err := CheckLockout(context.Background(), e.d, []contract.FirewallRule{tc.rule})
		var ae *contract.AgentError
		switch {
		case tc.ok && err != nil:
			t.Errorf("%s: refused: %v", name, err)
		case !tc.ok && (!errors.As(err, &ae) || ae.Code != contract.ErrValidationFailed):
			t.Errorf("%s: CheckLockout = %v, want validation_failed", name, err)
		}
	}
}

func TestDryRunFailureChangesNothing(t *testing.T) {
	v4, v6 := installerRules()
	bad := ufwScript("--dry-run", "allow", "proto", "tcp", "from", "any", "to", "any", "port", "22", "comment", "zimam:ssh-22")
	bad.Result.ExitCode = 1
	e := newEnv(t, v4, v6, sshdT("22"), bad)
	_, _, err := Apply(context.Background(), e.d, nil)
	if err == nil {
		t.Fatal("Apply succeeded after a failed dry run")
	}
	for _, c := range e.run.Calls() {
		if c.Path == ufw && c.Args[0] != "--dry-run" || c.Path == "/usr/bin/systemd-run" {
			t.Errorf("ran %s %q after the dry run failed", c.Path, c.Args)
		}
	}
	if entries, _ := os.ReadDir(e.d.Paths.ChangesDir()); len(entries) != 0 {
		t.Errorf("left change records %v", entries)
	}
}

func TestApplyRunsDryRunsThenGuardedCalls(t *testing.T) {
	v4, v6 := installerRules()
	adopt := func(port, id string) []string {
		return []string{"allow", "proto", "tcp", "from", "any", "to", "any", "port", port, "comment", "zimam:" + id}
	}
	calls := [][]string{adopt("22", "ssh-22"), adopt("80", "http"), adopt("443", "https")}
	var scripts []sysexec.Script
	for _, c := range calls {
		scripts = append(scripts, ufwScript(append([]string{"--dry-run"}, c...)...), ufwScript(c...))
	}
	scripts = append(scripts, sysexec.Script{Path: "/usr/bin/systemd-run", AnyArgs: true}, sshdT("22"))
	e := newEnv(t, v4, v6, scripts...)
	id, deadline, err := Apply(context.Background(), e.d, []contract.FirewallRule{})
	if err != nil {
		t.Fatal(err)
	}
	if id.Validate() != nil || !deadline.Equal(e.d.Now().Add(Window)) {
		t.Errorf("Apply = %q, %v", id, deadline)
	}
	var sawTimer bool
	for _, c := range e.run.Calls() {
		switch {
		case c.Path == "/usr/bin/systemd-run":
			sawTimer = true
		case c.Path == ufw && c.Args[0] != "--dry-run" && !sawTimer:
			t.Errorf("ufw %q ran before the revert timer started", c.Args)
		}
		// TestArgvHasNoShell: every argument is a plain word.
		for _, a := range c.Args {
			if strings.ContainsAny(a, " ;|&$`'\"\n") {
				t.Errorf("%s argument %q", c.Path, a)
			}
		}
		if c.Path == "/bin/sh" || c.Path == "/usr/bin/bash" {
			t.Errorf("ran a shell: %v", c)
		}
	}
	if _, err := os.Stat(e.d.Paths.ChangeRecord(id)); err != nil {
		t.Errorf("no change record: %v", err)
	}
}

func TestRevertRestoresPreviousTaggedRules(t *testing.T) {
	v4, v6 := installerRules()
	e := newEnv(t, v4, v6, ufwScript("reload"))
	prev, err := Snapshot(e.d)
	if err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(e.d.Paths.UFWUserRules())
	if err := os.WriteFile(e.d.Paths.UFWUserRules(), []byte("changed"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := Restore(context.Background(), e.d, prev); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(e.d.Paths.UFWUserRules())
	fi, _ := os.Stat(e.d.Paths.UFWUserRules())
	if string(after) != string(before) || fi.Mode().Perm() != 0o640 {
		t.Errorf("restored %q (%v)", after, fi.Mode())
	}
	if last := e.run.Calls()[len(e.run.Calls())-1]; last.Path != ufw || !slices.Equal(last.Args, []string{"reload"}) {
		t.Errorf("last call %v, want ufw reload", last)
	}
}

func TestStatusGroupsRules(t *testing.T) {
	v4, v6 := installerRules()
	v4 += tupleLine("allow", "tcp", "8080", "203.0.113.0/24", "zimam:app") + tupleLine("deny", "udp", "53", "198.51.100.7", "zimam:dns") +
		tupleLine("allow", "tcp", "22", "0.0.0.0/0", "zimam:ssh-22") + tupleLine("allow", "tcp", "9000:9010", "0.0.0.0/0", "zimam:range")
	v6 += tupleLine("allow", "tcp", "8080", "2001:db8::/32", "zimam:app") + tupleLine("allow", "tcp", "22", "::/0", "zimam:ssh-22")
	e := newEnv(t, v4, v6, ufwActiveScript())
	s, err := Status(context.Background(), e.d)
	if err != nil {
		t.Fatal(err)
	}
	if !s.Active || len(s.External) != 6 || len(s.Locked) != 1 || len(s.Rules) != 3 {
		t.Fatalf("status = %+v", s)
	}
	app := s.Rules[0]
	if app.ID != "app" || !slices.Equal(app.From, contract.IPList{"203.0.113.0/24", "2001:db8::/32"}) {
		t.Errorf("app = %+v", app)
	}
	if dns := s.Rules[1]; dns.Action != "deny" || dns.Proto != "udp" || !slices.Equal(dns.From, contract.IPList{"198.51.100.7"}) {
		t.Errorf("dns = %+v", dns)
	}
	if r := s.Rules[2]; r.PortFrom != 9000 || r.PortTo != 9010 || len(r.From) != 0 {
		t.Errorf("range = %+v", r)
	}
	if s.Locked[0].ID != "ssh-22" || len(s.Locked[0].From) != 0 {
		t.Errorf("locked = %+v", s.Locked)
	}
	// A status rule is valid params again, so the panel can round-trip it.
	if err := (&contract.FirewallRulesParams{Rules: s.Rules}).Validate(); err != nil {
		t.Errorf("status rules don't validate: %v", err)
	}
}

// ufw.conf can say ENABLED=yes while UFW isn't running (an LXD container
// installed with --skip-firewall): active is what ufw status says.
func TestStatusActiveIsUFWsOwnAnswer(t *testing.T) {
	inactive := ufwScript("status")
	inactive.Result.Stdout = []byte("Status: inactive\n")
	e := newEnv(t, "", "", inactive)
	if s, err := Status(context.Background(), e.d); err != nil || s.Active {
		t.Errorf("Status = active %v, %v; want inactive", s.Active, err)
	}
}

func TestSSHPorts(t *testing.T) {
	e := newEnv(t, "", "", sshdT("22", "2222", "22"))
	ports, err := SSHPorts(context.Background(), e.d)
	if err != nil || !slices.Equal(ports, []contract.TCPPort{22, 2222}) {
		t.Errorf("SSHPorts = %v, %v", ports, err)
	}
	// Without it sshd -T refuses to run (socket-activated ssh, seen on a VPS).
	if fi, err := os.Stat(e.d.Paths.SSHDRunDir()); err != nil || !fi.IsDir() {
		t.Errorf("SSHPorts left no %s: %v", e.d.Paths.SSHDRunDir(), err)
	}
	e = newEnv(t, "", "", sysexec.Script{Path: sshd, Args: []string{"-T"}, Result: sysexec.Result{Stdout: []byte("port 0\n")}})
	if _, err := SSHPorts(context.Background(), e.d); err == nil {
		t.Error("SSHPorts accepted port 0")
	}
}

func TestOriginRestrictionReplacesWebRules(t *testing.T) {
	// The installer's untagged web rules would keep 80 and 443 open to
	// everyone, so they go too; the owner's other rules stay.
	v4, v6 := installerRules()
	v4 += tupleLine("allow", "tcp", "8080", "0.0.0.0/0", "")
	tuples := append(parseTuples([]byte(v4), false), parseTuples([]byte(v6), true)...)
	got, err := webCalls(contract.IPList{"173.245.48.0/20", "2400:cb00::/32"}, tuples, true)
	if err != nil {
		t.Fatal(err)
	}
	add := func(src, port, id string) []string {
		return []string{"allow", "proto", "tcp", "from", src, "to", "any", "port", port, "comment", "zimam:" + id}
	}
	del := func(port string) []string {
		return []string{"delete", "allow", "proto", "tcp", "from", "any", "to", "any", "port", port}
	}
	// Adds first, so the sites never lose their ports.
	want := [][]string{
		add("173.245.48.0/20", "80", "http"), add("2400:cb00::/32", "80", "http"),
		add("173.245.48.0/20", "443", "https"), add("2400:cb00::/32", "443", "https"),
		del("80"), del("443"),
	}
	if !slices.EqualFunc(got, want, slices.Equal) {
		t.Errorf("webCalls =\n%q\nwant\n%q", got, want)
	}

	// Lifting it again: anywhere first, then each range goes.
	cf := tupleLine("allow", "tcp", "80", "173.245.48.0/20", "zimam:http") + tupleLine("allow", "tcp", "443", "173.245.48.0/20", "zimam:https")
	tuples = parseTuples([]byte(cf), false)
	got, err = webCalls(contract.IPList{}, tuples, false)
	if err != nil {
		t.Fatal(err)
	}
	want = [][]string{
		add("any", "80", "http"), add("any", "443", "https"),
		{"delete", "allow", "proto", "tcp", "from", "173.245.48.0/20", "to", "any", "port", "80"},
		{"delete", "allow", "proto", "tcp", "from", "173.245.48.0/20", "to", "any", "port", "443"},
	}
	if !slices.EqualFunc(got, want, slices.Equal) {
		t.Errorf("webCalls =\n%q\nwant\n%q", got, want)
	}
}

func TestWebRestricted(t *testing.T) {
	v4, v6 := installerRules()
	e := newEnv(t, v4, v6)
	if on, err := WebRestricted(e.d); err != nil || on {
		t.Errorf("installer rules: WebRestricted = %v, %v", on, err)
	}
	e = newEnv(t, tupleLine("allow", "tcp", "443", "173.245.48.0/20", "zimam:https"), "")
	if on, err := WebRestricted(e.d); err != nil || !on {
		t.Errorf("Cloudflare-only https: WebRestricted = %v, %v", on, err)
	}
}

func TestLiftingRestrictionAdoptsInstallerWebRules(t *testing.T) {
	v4, v6 := installerRules()
	tuples := append(parseTuples([]byte(v4), false), parseTuples([]byte(v6), true)...)
	got, err := webCalls(contract.IPList{}, tuples, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, args := range got {
		if args[0] == "delete" {
			t.Errorf("deletes the rule it adopts: %q", args)
		}
	}
	if len(got) != 2 {
		t.Errorf("webCalls = %q, want the two adoptions", got)
	}
}

func ufwActiveScript() sysexec.Script {
	s := ufwScript("status")
	s.Result.Stdout = []byte("Status: active\n")
	return s
}
