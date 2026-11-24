package contract

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestFirewallValues(t *testing.T) {
	checkValues(t,
		[]FirewallRuleID{"app", "app-8080", FirewallRuleID("a" + strings.Repeat("b", 23))},
		[]FirewallRuleID{"", "a", "1a", "-a", "a-", "App", "a_b", "a b", "zimam:a", "a\n", FirewallRuleID("a" + strings.Repeat("b", 24))})
	checkValues(t,
		[]FirewallAction{"allow", "deny", "limit"},
		[]FirewallAction{"", "reject", "Allow", "allow "})
	checkValues(t,
		[]FirewallProto{"tcp", "udp"},
		[]FirewallProto{"", "any", "icmp", "TCP"})
	checkValues(t,
		[]ChangeID{"01JD5Z3W4X6Y7Z8A9B0C1D2E3F"},
		[]ChangeID{"", "01jd5z3w4x6y7z8a9b0c1d2e3f", "01JD5Z3W4X6Y7Z8A9B0C1D2E3", "01JD5Z3W4X6Y7Z8A9B0C1D2E3I"})
}

func appRule() FirewallRule {
	return FirewallRule{ID: "app", Action: "allow", Proto: "tcp", PortFrom: 8080, PortTo: 8080, From: IPList{"203.0.113.0/24", "2001:db8::/32"}}
}

func TestFirewallRule(t *testing.T) {
	mod := func(f func(*FirewallRule)) FirewallRule { r := appRule(); f(&r); return r }
	for name, r := range map[string]FirewallRule{
		"one port":   appRule(),
		"anywhere":   mod(func(r *FirewallRule) { r.From = IPList{} }),
		"range":      mod(func(r *FirewallRule) { r.PortFrom, r.PortTo = 9000, 9010 }),
		"limit":      mod(func(r *FirewallRule) { r.Action = "limit" }),
		"deny udp":   mod(func(r *FirewallRule) { r.Action, r.Proto = "deny", "udp" }),
		"edge ports": mod(func(r *FirewallRule) { r.PortFrom, r.PortTo = 1, 65535 }),
	} {
		if err := r.Validate(); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	for name, r := range map[string]FirewallRule{
		"bad id":       mod(func(r *FirewallRule) { r.ID = "A" }),
		"bad action":   mod(func(r *FirewallRule) { r.Action = "reject" }),
		"bad proto":    mod(func(r *FirewallRule) { r.Proto = "any" }),
		"port 0":       mod(func(r *FirewallRule) { r.PortFrom, r.PortTo = 0, 0 }),
		"port 65536":   mod(func(r *FirewallRule) { r.PortTo = 65536 }),
		"from > to":    mod(func(r *FirewallRule) { r.PortFrom, r.PortTo = 9010, 9000 }),
		"host bits":    mod(func(r *FirewallRule) { r.From = IPList{"203.0.113.5/24"} }),
		"not an ip":    mod(func(r *FirewallRule) { r.From = IPList{"example.com"} }),
		"nil from":     mod(func(r *FirewallRule) { r.From = nil }),
		"dup source":   mod(func(r *FirewallRule) { r.From = IPList{"192.0.2.1", "192.0.2.1/32"} }),
		"many sources": mod(func(r *FirewallRule) { r.From = manySources(21) }),
	} {
		if err := r.Validate(); err == nil {
			t.Errorf("%s: Validate accepted %+v", name, r)
		}
	}
}

func manySources(n int) IPList {
	l := make(IPList, n)
	for i := range l {
		l[i] = fmt.Sprintf("192.0.2.%d", i+1)
	}
	return l
}

func TestFirewallRulesParams(t *testing.T) {
	web := FirewallRule{ID: "admin", Action: "deny", Proto: "tcp", PortFrom: 8080, PortTo: 8080, From: IPList{"198.51.100.7"}}
	for name, p := range map[string]FirewallRulesParams{
		"none":    {Rules: []FirewallRule{}},
		"two":     {Rules: []FirewallRule{appRule(), web}},
		"hundred": {Rules: hundredRules()},
	} {
		if err := p.Validate(); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	same := appRule()
	same.ID = "app2"
	overlap := appRule()
	overlap.ID, overlap.Action, overlap.From = "app3", "deny", IPList{"203.0.113.0/24"}
	wide := FirewallRule{ID: "all", Action: "allow", Proto: "tcp", PortFrom: 3000, PortTo: 3000, From: IPList{}}
	wide2 := wide
	wide2.ID = "all2"
	for name, p := range map[string]FirewallRulesParams{
		"nil rules":       {},
		"duplicate id":    {Rules: []FirewallRule{appRule(), appRule()}},
		"same rule twice": {Rules: []FirewallRule{appRule(), same}},
		"shared source":   {Rules: []FirewallRule{appRule(), overlap}},
		"both anywhere":   {Rules: []FirewallRule{wide, wide2}},
		"locked http":     {Rules: []FirewallRule{{ID: "http", Action: "allow", Proto: "tcp", PortFrom: 80, PortTo: 80, From: IPList{}}}},
		"locked https":    {Rules: []FirewallRule{{ID: "https", Action: "allow", Proto: "tcp", PortFrom: 443, PortTo: 443, From: IPList{}}}},
		"locked ssh":      {Rules: []FirewallRule{{ID: "ssh-2222", Action: "allow", Proto: "tcp", PortFrom: 2222, PortTo: 2222, From: IPList{}}}},
		"bad rule":        {Rules: []FirewallRule{{ID: "x1", Action: "allow", Proto: "tcp", PortFrom: 0, PortTo: 0, From: IPList{}}}},
		"too many rules":  {Rules: append(hundredRules(), FirewallRule{ID: "extra", Action: "allow", Proto: "udp", PortFrom: 1, PortTo: 1, From: IPList{}})},
		"too many ufw":    {Rules: tooManyEntries()},
	} {
		if err := p.Validate(); err == nil {
			t.Errorf("%s: Validate accepted %d rules", name, len(p.Rules))
		}
	}
}

func hundredRules() []FirewallRule {
	r := make([]FirewallRule, 100)
	for i := range r {
		r[i] = FirewallRule{ID: FirewallRuleID(fmt.Sprintf("r%d", i)), Action: "allow", Proto: "tcp", PortFrom: TCPPort(10000 + i), PortTo: TCPPort(10000 + i), From: IPList{}}
	}
	return r
}

// tooManyEntries is 11 rules of 20 sources: 220 UFW rules, over the cap.
func tooManyEntries() []FirewallRule {
	r := make([]FirewallRule, 11)
	for i := range r {
		r[i] = FirewallRule{ID: FirewallRuleID(fmt.Sprintf("r%d", i)), Action: "allow", Proto: "tcp", PortFrom: TCPPort(10000 + i), PortTo: TCPPort(10000 + i), From: manySources(20)}
	}
	return r
}

func TestLockedFirewallRule(t *testing.T) {
	for id, want := range map[FirewallRuleID]bool{"http": true, "https": true, "ssh-22": true, "ssh-2222": true, "ssh": false, "sshd": false, "http2": false, "app": false} {
		if got := LockedFirewallRule(id); got != want {
			t.Errorf("LockedFirewallRule(%q) = %v, want %v", id, got, want)
		}
	}
}

func TestChangeConfirmParams(t *testing.T) {
	if err := (&ChangeConfirmParams{Change: "01JD5Z3W4X6Y7Z8A9B0C1D2E3F"}).Validate(); err != nil {
		t.Error(err)
	}
	if (&ChangeConfirmParams{Change: "x"}).Validate() == nil {
		t.Error("accepted a change id that isn't a ULID")
	}
}

func TestCloudflareModeParams(t *testing.T) {
	for _, p := range []CloudflareModeParams{{}, {Enabled: true}, {Enabled: true, RestrictOrigin: true}} {
		if err := p.Validate(); err != nil {
			t.Errorf("%+v: %v", p, err)
		}
	}
	if (&CloudflareModeParams{RestrictOrigin: true}).Validate() == nil {
		t.Error("accepted restrict_origin without Cloudflare mode")
	}
}

// The guarded commands answer with the change to confirm and its deadline.
func TestGuardedResultsCarryChange(t *testing.T) {
	for name, result := range map[string]any{"firewall.rules": FirewallRulesResult{}, "ssh.settings": SSHSettingsResult{}, "cloudflare.mode": CloudflareModeResult{}} {
		v := jsonValue(t, withChange(result)).(map[string]any)
		if v["change"] != "01JD5Z3W4X6Y7Z8A9B0C1D2E3F" || v["deadline"] == nil {
			t.Errorf("%s: %v has no change and deadline", name, v)
		}
	}
}

func withChange(r any) any {
	id, at := ChangeID("01JD5Z3W4X6Y7Z8A9B0C1D2E3F"), time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	switch r := r.(type) {
	case FirewallRulesResult:
		r.Change, r.Deadline = id, at
		return r
	case SSHSettingsResult:
		r.Change, r.Deadline = id, at
		return r
	case CloudflareModeResult:
		r.Change, r.Deadline = &id, &at
		return r
	}
	return nil
}

func TestServerSecuritySubjects(t *testing.T) {
	for _, name := range []string{"firewall.rules", "ssh.settings", "fail2ban.config", "fail2ban.unban", "cloudflare.mode"} {
		d, ok := Lookup(name)
		if !ok {
			t.Fatalf("%s isn't registered", name)
		}
		if got := d.Subject(d.Params()); got != "server" {
			t.Errorf("%s subject = %q, want server", name, got)
		}
	}
}
