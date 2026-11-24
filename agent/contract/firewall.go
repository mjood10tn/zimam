package contract

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

// The firewall commands (v0.6.0 plan, "New agent commands"). firewall.rules
// is a guarded change (R06-D1): it answers with the change to confirm.
func init() {
	Register(Descriptor{
		Name:    "firewall.rules",
		Kind:    KindJob,
		Audited: true,
		Params:  func() Params { return &FirewallRulesParams{} },
		Result:  FirewallRulesResult{},
		Subject: func(Params) string { return "server" },
	})
	Register(Descriptor{
		Name:   "firewall.status",
		Kind:   KindRead,
		Params: func() Params { return &FirewallStatusParams{} },
		Result: FirewallStatusResult{},
	})
}

const (
	maxFirewallRules = 100
	// maxRuleSources and maxFirewallEntries bound the ufw calls one
	// firewall.rules makes: one per source (or per family for "anywhere"),
	// each with a dry run, inside the job's 120 s.
	maxRuleSources     = 20
	maxFirewallEntries = 200
)

// FirewallRuleID is the <id> in a rule's zimam:<id> UFW comment (R06-D3).
type FirewallRuleID string

func (id FirewallRuleID) Validate() error { return backupName(string(id)) }
func (FirewallRuleID) JSONSchema() Schema {
	return Schema{"type": "string", "pattern": backupIDNameRe.String()}
}

// LockedFirewallRule reports the ids the agent keeps for itself: one ssh-<port>
// rule per SSH port, and http and https (R06-D3, D9). firewall.rules can't
// set, remove or narrow them.
func LockedFirewallRule(id FirewallRuleID) bool {
	return id == "http" || id == "https" || strings.HasPrefix(string(id), "ssh-")
}

type FirewallAction string

var firewallActions = []FirewallAction{"allow", "deny", "limit"}

func (a FirewallAction) Validate() error {
	if slices.Contains(firewallActions, a) {
		return nil
	}
	return errors.New("must be allow, deny or limit")
}

func (FirewallAction) JSONSchema() Schema { return Schema{"type": "string", "enum": firewallActions} }

type FirewallProto string

var firewallProtos = []FirewallProto{"tcp", "udp"}

func (p FirewallProto) Validate() error {
	if slices.Contains(firewallProtos, p) {
		return nil
	}
	return errors.New("must be tcp or udp")
}

func (FirewallProto) JSONSchema() Schema { return Schema{"type": "string", "enum": firewallProtos} }

// FirewallRule is one rule the owner manages: a port or a port range, from
// anywhere (an empty From) or from a list of canonical addresses and prefixes.
// A single port has PortFrom == PortTo.
type FirewallRule struct {
	ID       FirewallRuleID `json:"id"`
	Action   FirewallAction `json:"action"`
	Proto    FirewallProto  `json:"proto"`
	PortFrom TCPPort        `json:"port_from"`
	PortTo   TCPPort        `json:"port_to"`
	From     IPList         `json:"from"`
}

func (r FirewallRule) Validate() error {
	f := FieldErrors{}
	r.check(f, "")
	return fieldsError(f)
}

func (r FirewallRule) check(f FieldErrors, prefix string) {
	f.Check(prefix+"id", r.ID)
	f.Check(prefix+"action", r.Action)
	f.Check(prefix+"proto", r.Proto)
	f.Check(prefix+"port_from", r.PortFrom)
	f.Check(prefix+"port_to", r.PortTo)
	if r.PortFrom > r.PortTo {
		f[prefix+"port_to"] = "must not be below port_from"
	}
	switch {
	case r.From == nil:
		f[prefix+"from"] = "is required (an empty list for anywhere)"
	case len(r.From) > maxRuleSources:
		f[prefix+"from"] = fmt.Sprintf("must have at most %d entries", maxRuleSources)
	default:
		f.Check(prefix+"from", r.From)
	}
	if _, ok := f[prefix+"from"]; !ok && r.sourceClash() {
		f[prefix+"from"] = "lists the same address twice"
	}
}

// sources are the rule's sources as UFW sees them: "" for anywhere.
func (r FirewallRule) sources() []string {
	if len(r.From) == 0 {
		return []string{""}
	}
	out := make([]string, len(r.From))
	for i, e := range r.From {
		p, _ := ParseIPEntry(e)
		out[i] = p.String()
	}
	return out
}

func (r FirewallRule) sourceClash() bool {
	s := r.sources()
	slices.Sort(s)
	return len(slices.Compact(s)) != len(s)
}

type FirewallRulesParams struct {
	Rules []FirewallRule `json:"rules"`
}

func (p *FirewallRulesParams) Validate() error {
	f := FieldErrors{}
	switch {
	case p.Rules == nil:
		f["rules"] = "is required (an empty list for none)"
	case len(p.Rules) > maxFirewallRules:
		f["rules"] = fmt.Sprintf("must have at most %d rules", maxFirewallRules)
	}
	ids := map[FirewallRuleID]bool{}
	// UFW keeps one rule per proto, ports and source whatever its action or
	// comment, so two of ours sharing one would delete each other (R06-D3).
	tuples := map[string]FirewallRuleID{}
	entries := 0
	for i, r := range p.Rules {
		prefix := fmt.Sprintf("rules.%d.", i)
		r.check(f, prefix)
		switch {
		case LockedFirewallRule(r.ID):
			f[prefix+"id"] = "is kept by the agent"
		case ids[r.ID]:
			f[prefix+"id"] = "is used twice"
		}
		ids[r.ID] = true
		for _, s := range r.sources() {
			entries++
			key := fmt.Sprintf("%s %d:%d %s", r.Proto, r.PortFrom, r.PortTo, s)
			if other, ok := tuples[key]; ok && other != r.ID {
				f[prefix+"from"] = fmt.Sprintf("overlaps rule %s (same proto, ports and source)", other)
			}
			tuples[key] = r.ID
		}
	}
	if entries > maxFirewallEntries {
		f["rules"] = fmt.Sprintf("must add up to at most %d sources", maxFirewallEntries)
	}
	return f.Err()
}

// FirewallRulesResult is the guarded change to confirm before Deadline.
type FirewallRulesResult struct {
	Change   ChangeID  `json:"change"`
	Deadline time.Time `json:"deadline"`
}

type FirewallStatusParams struct{}

func (*FirewallStatusParams) Validate() error { return nil }

// FirewallStatusResult lists UFW's rules: the owner's (Rules), the agent's
// own (Locked) and the ones Zimam didn't make (External), which it never
// changes (R06-D3).
type FirewallStatusResult struct {
	Active   bool                   `json:"active"`
	Rules    []FirewallRule         `json:"rules"`
	Locked   []FirewallRule         `json:"locked"`
	External []ExternalFirewallRule `json:"external"`
}

// ExternalFirewallRule is a rule as UFW stores it; Port and From are UFW's
// own text ("22", "9000:9010", "any").
type ExternalFirewallRule struct {
	Action string `json:"action"`
	Proto  string `json:"proto"`
	Port   string `json:"port"`
	From   string `json:"from"`
	IPv6   bool   `json:"ipv6"`
}
