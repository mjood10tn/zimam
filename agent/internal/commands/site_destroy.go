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
	jobPrechecks["site.destroy"] = func(d sysexec.Deps) jobs.Precheck {
		return func(_ context.Context, p contract.Params) error {
			return sites.CheckDestroy(d, helper.LookupAccount, p.(*contract.SiteNameParams).Name)
		}
	}
	jobHandlers["site.destroy"] = siteDestroy
}

// siteDestroy removes the site (§9.8). A site already gone answers
// not_found, as the precheck does.
func siteDestroy(ctx context.Context, j *jobs.Context, p contract.Params) (any, error) {
	name := p.(*contract.SiteNameParams).Name
	d := j.Deps()
	if err := sites.CheckDestroy(d, helper.LookupAccount, name); err != nil {
		return nil, err
	}
	return sites.Destroy(ctx, d, j.Helper, helper.LookupAccount, name)
}
