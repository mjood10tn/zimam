package commands

import (
	"context"

	"zimam/agent/contract"
	"zimam/agent/internal/deploy"
	"zimam/agent/internal/helper"
	"zimam/agent/internal/jobs"
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
	return deploy.WriteEnv(ctx, j.Deps(), j.Helper, helper.LookupAccount, *p.(*contract.SiteEnvWriteParams))
}
