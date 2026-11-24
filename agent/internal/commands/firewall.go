package commands

import (
	"context"

	"zimam/agent/contract"
	"zimam/agent/internal/firewall"
	"zimam/agent/internal/jobs"
	"zimam/agent/internal/sysexec"
)

func init() {
	// The lockout check needs sshd -T, so it runs at submit as well as in
	// the job: the panel hears about a blocked SSH port at once.
	jobPrechecks["firewall.rules"] = func(d sysexec.Deps) jobs.Precheck {
		return func(ctx context.Context, p contract.Params) error {
			return firewall.CheckLockout(ctx, d, p.(*contract.FirewallRulesParams).Rules)
		}
	}
	jobHandlers["firewall.rules"] = firewallRules
}

// firewallRules applies the owner's rules as a guarded change (R06-D1, D3).
func firewallRules(ctx context.Context, j *jobs.Context, p contract.Params) (any, error) {
	id, deadline, err := firewall.Apply(ctx, j.Deps(), p.(*contract.FirewallRulesParams).Rules)
	if err != nil {
		return nil, err
	}
	return contract.FirewallRulesResult{Change: id, Deadline: deadline}, nil
}

func firewallStatus(d sysexec.Deps) Handler {
	return func(context.Context, contract.Params) (any, error) { return firewall.Status(d) }
}
