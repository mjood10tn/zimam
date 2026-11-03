package commands

import (
	"context"

	"zimam/agent/contract"
	"zimam/agent/internal/helper"
	"zimam/agent/internal/jobs"
	"zimam/agent/internal/sites"
	"zimam/agent/internal/sysexec"
)

func init() {
	jobPrechecks["site.provision"] = func(d sysexec.Deps) jobs.Precheck {
		return func(_ context.Context, p contract.Params) error {
			return sites.Check(d, *p.(*contract.SiteProvisionParams))
		}
	}
	jobHandlers["site.provision"] = siteProvision
}

// siteProvision checks again, since another site may have taken the domain
// since the job was accepted, then provisions the site (§9.2).
func siteProvision(ctx context.Context, j *jobs.Context, p contract.Params) (any, error) {
	params := *p.(*contract.SiteProvisionParams)
	d := j.Deps()
	if err := sites.Check(d, params); err != nil {
		return nil, err
	}
	return sites.Provision(ctx, d, j.Helper, helper.LookupAccount, params)
}
