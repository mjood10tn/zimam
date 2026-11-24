package firewall

import (
	"context"
	"strconv"
	"strings"

	"zimam/agent/contract"
	"zimam/agent/internal/sysexec"
)

// Status lists UFW's rules from its own files: Zimam's rules grouped back by
// id, the locked ones apart, and the rest as external. Whether UFW is on is
// ufw status's answer: ufw.conf can say ENABLED=yes while UFW isn't running
// (an LXD container installed with --skip-firewall).
func Status(ctx context.Context, d sysexec.Deps) (contract.FirewallStatusResult, error) {
	res := contract.FirewallStatusResult{Rules: []contract.FirewallRule{}, Locked: []contract.FirewallRule{}, External: []contract.ExternalFirewallRule{}}
	out, err := d.Run.Run(ctx, sysexec.Cmd{Path: ufw, Args: []string{"status"}})
	if err != nil {
		return res, err
	}
	res.Active = strings.Contains(string(out.Stdout), "Status: active")
	tuples, err := readTuples(d.Paths)
	if err != nil {
		return res, err
	}
	var order []contract.FirewallRuleID
	byID := map[contract.FirewallRuleID]*contract.FirewallRule{}
	for _, t := range tuples {
		if t.ID == "" {
			res.External = append(res.External, contract.ExternalFirewallRule{Action: t.Action, Proto: t.Proto, Port: t.Port, From: t.From, IPv6: t.IPv6})
			continue
		}
		r := byID[t.ID]
		if r == nil {
			from, to := parsePorts(t.Port)
			r = &contract.FirewallRule{ID: t.ID, Action: contract.FirewallAction(t.Action), Proto: contract.FirewallProto(t.Proto), PortFrom: from, PortTo: to, From: contract.IPList{}}
			byID[t.ID] = r
			order = append(order, t.ID)
		}
		if !t.anywhere() {
			if p, err := contract.ParseIPEntry(t.From); err == nil {
				src := p.String()
				if p.IsSingleIP() {
					src = p.Addr().String()
				}
				r.From = append(r.From, src)
			}
		}
	}
	for _, id := range order {
		if contract.LockedFirewallRule(id) {
			res.Locked = append(res.Locked, *byID[id])
		} else {
			res.Rules = append(res.Rules, *byID[id])
		}
	}
	return res, nil
}

func parsePorts(s string) (contract.TCPPort, contract.TCPPort) {
	a, b, ok := strings.Cut(s, ":")
	from, _ := strconv.Atoi(a)
	if !ok {
		return contract.TCPPort(from), contract.TCPPort(from)
	}
	to, _ := strconv.Atoi(b)
	return contract.TCPPort(from), contract.TCPPort(to)
}
