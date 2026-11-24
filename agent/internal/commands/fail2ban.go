package commands

import (
	"context"

	"zimam/agent/contract"
	"zimam/agent/internal/fail2ban"
	"zimam/agent/internal/jobs"
	"zimam/agent/internal/sysexec"
)

func init() {
	jobHandlers["fail2ban.config"] = func(ctx context.Context, j *jobs.Context, p contract.Params) (any, error) {
		return fail2ban.Configure(ctx, j.Deps(), *p.(*contract.Fail2banConfigParams))
	}
	jobPrechecks["fail2ban.unban"] = func(d sysexec.Deps) jobs.Precheck {
		return func(context.Context, contract.Params) error {
			if ok, err := fail2ban.Installed(d); err != nil || ok {
				return err
			}
			return &contract.AgentError{Code: contract.ErrNotFound, Message: "fail2ban isn't installed."}
		}
	}
	jobHandlers["fail2ban.unban"] = func(ctx context.Context, j *jobs.Context, p contract.Params) (any, error) {
		u := p.(*contract.Fail2banUnbanParams)
		ok, err := fail2ban.Unban(ctx, j.Deps(), u.Jail, u.Address)
		if err != nil {
			return nil, err
		}
		return contract.Fail2banUnbanResult{Jail: u.Jail, Address: u.Address, Unbanned: ok}, nil
	}
}

func fail2banStatus(d sysexec.Deps) Handler {
	return func(ctx context.Context, _ contract.Params) (any, error) { return fail2ban.Status(ctx, d) }
}
