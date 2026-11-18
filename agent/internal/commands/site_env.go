package commands

import (
	"context"
	"fmt"

	"zimam/agent/contract"
	"zimam/agent/internal/deploy"
	"zimam/agent/internal/helper"
	"zimam/agent/internal/jobs"
	"zimam/agent/internal/siteprocs"
	"zimam/agent/internal/sites"
	"zimam/agent/internal/sysexec"
)

func init() {
	jobPrechecks["site.env.write"] = func(d sysexec.Deps) jobs.Precheck {
		return func(_ context.Context, p contract.Params) error {
			return deploy.CheckEnvWrite(d, *p.(*contract.SiteEnvWriteParams))
		}
	}
	jobHandlers["site.env.write"] = siteEnvWrite
}

// siteEnvRead answers site.env.read: the site's shared .env, through the
// installed agent binary's helper. It returns a secret, so the dispatcher
// audits it, with the content redacted (R02-D10).
func siteEnvRead(d sysexec.Deps) Handler {
	return func(ctx context.Context, p contract.Params) (any, error) {
		h := &helper.Client{AgentBinary: d.Paths.AgentBinary()}
		return deploy.ReadEnv(ctx, d, h, helper.LookupAccount, p.(*contract.SiteNameParams).Name)
	}
}

// siteEnvWrite replaces the site's shared .env. deploy.WriteEnv checks
// again first: the site may have been destroyed since the job was accepted.
func siteEnvWrite(ctx context.Context, j *jobs.Context, p contract.Params) (any, error) {
	res, err := deploy.WriteEnv(ctx, j.Deps(), j.Helper, helper.LookupAccount, *p.(*contract.SiteEnvWriteParams))
	if err != nil {
		return res, err
	}
	return afterEnvWrite(ctx, j.Deps(), res)
}

// afterEnvWrite restarts the site's workers once its .env is saved, since
// they hold its config in memory: a rotated password would otherwise leave
// them on dead credentials (R04-D10). A failed restart fails the job with
// the .env saved (jobs.Landed); writing it again restarts them again.
func afterEnvWrite(ctx context.Context, d sysexec.Deps, res contract.SiteNameResult) (any, error) {
	st, err := sites.Load(d.Paths, res.Name)
	if err == nil {
		err = siteprocs.RestartWorkers(ctx, d, st)
	}
	if err != nil {
		return res, &jobs.Landed{Err: fmt.Errorf("site %s's .env is saved, but restarting its workers failed: %w", res.Name, err)}
	}
	return res, nil
}
