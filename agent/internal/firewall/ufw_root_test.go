//go:build roottest

package firewall

import (
	"context"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"

	"zimam/agent/contract"
	"zimam/agent/internal/changes"
	"zimam/agent/internal/paths"
	"zimam/agent/internal/sysexec"
)

// TestRulesAgainstRealUFW runs a guarded change against the real UFW on a
// throwaway server (it rewrites /etc/ufw): the installer's rules are adopted,
// the owner's rules added in both families, a deny goes first, and the
// revert puts every rule back.
func TestRulesAgainstRealUFW(t *testing.T) {
	if os.Getuid() != 0 {
		t.Skip("needs root")
	}
	sh := func(args ...string) {
		t.Helper()
		if out, err := exec.Command(args[0], args[1:]...).CombinedOutput(); err != nil {
			t.Fatalf("%v: %v\n%s", args, err, out)
		}
	}
	// The e2e run calls this on its server (m1-roottests.sh), whose later
	// steps expect UFW as the installer left it, off under --skip-firewall:
	// /etc/ufw and UFW's state go back exactly when the test ends.
	backup := t.TempDir() + "/ufw.tar"
	sh("tar", "-C", "/etc", "-cf", backup, "ufw")
	wasActive := exec.Command("sh", "-c", "ufw status | grep -q '^Status: active'").Run() == nil
	t.Cleanup(func() {
		sh("ufw", "--force", "disable")
		sh("rm", "-rf", "/etc/ufw")
		sh("tar", "-C", "/etc", "-xpf", backup)
		if wasActive {
			sh("ufw", "--force", "enable")
		}
	})
	sh("ufw", "--force", "reset")
	for _, port := range []string{"22/tcp", "80/tcp", "443/tcp"} {
		sh("ufw", "allow", port)
	}
	sh("ufw", "allow", "51820/udp") // the owner's own rule
	sh("ufw", "--force", "enable")
	p := paths.Paths{}
	if err := os.MkdirAll(p.LocksDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	// systemd-run checks the revert timer's program exists; the test reverts
	// before the timer fires.
	if _, err := os.Stat(p.AgentBinary()); os.IsNotExist(err) {
		if err := os.WriteFile(p.AgentBinary(), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		defer os.Remove(p.AgentBinary())
	}
	d := sysexec.Deps{Paths: p, Run: sysexec.ExecRunner{}, Logf: t.Logf, Now: time.Now}
	before, err := Status(context.Background(), d)
	if err != nil {
		t.Fatal(err)
	}
	rules := []contract.FirewallRule{
		{ID: "app", Action: "allow", Proto: "tcp", PortFrom: 8080, PortTo: 8080, From: contract.IPList{"203.0.113.0/24", "2001:db8::/32"}},
		{ID: "range", Action: "allow", Proto: "tcp", PortFrom: 9000, PortTo: 9010, From: contract.IPList{}},
		{ID: "block", Action: "deny", Proto: "tcp", PortFrom: 22, PortTo: 22, From: contract.IPList{"192.0.2.9"}},
		{ID: "lim", Action: "limit", Proto: "tcp", PortFrom: 2200, PortTo: 2200, From: contract.IPList{}},
	}
	id, _, err := Apply(context.Background(), d, rules)
	if err != nil {
		t.Fatal(err)
	}
	s, err := Status(context.Background(), d)
	if err != nil {
		t.Fatal(err)
	}
	var ids []contract.FirewallRuleID
	for _, r := range append(s.Locked, s.Rules...) {
		ids = append(ids, r.ID)
	}
	slices.Sort(ids)
	if !slices.Equal(ids, []contract.FirewallRuleID{"app", "block", "http", "https", "lim", "range", "ssh-22"}) {
		t.Errorf("tagged rules = %v", ids)
	}
	if len(s.External) != 2 || s.External[0].Port != "51820" || s.External[1].Port != "51820" || !s.External[1].IPv6 {
		t.Errorf("external = %+v, want only the owner's udp rule, in both families", s.External)
	}
	// The deny is the first v4 rule, before the SSH allow.
	if tuples, _ := readTuples(p); tuples[0].ID != "block" {
		t.Errorf("first rule = %+v, want the deny", tuples[0])
	}
	// Applying the same rules again changes nothing.
	tuples, _ := readTuples(p)
	ssh, _ := SSHPorts(context.Background(), d)
	if calls, err := plan(append(slices.Clone(rules), lockedRules(ssh, tuples)...), tuples, true); err != nil || len(calls) != 0 {
		t.Errorf("second plan = %q, %v", calls, err)
	}
	if err := changes.Revert(context.Background(), d, id); err != nil {
		t.Fatal(err)
	}
	after, err := Status(context.Background(), d)
	if err != nil {
		t.Fatal(err)
	}
	if len(after.Rules) != len(before.Rules) || len(after.Locked) != len(before.Locked) || len(after.External) != len(before.External) {
		t.Errorf("after revert: %+v\nbefore: %+v", after, before)
	}
	out, _ := exec.Command("iptables", "-S", "ufw-user-input").CombinedOutput()
	if strings.Contains(string(out), "8080") {
		t.Errorf("iptables still has the app rule:\n%s", out)
	}
}
