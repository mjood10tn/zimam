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
	jobPrechecks["site.repo"] = func(d sysexec.Deps) jobs.Precheck {
		return func(_ context.Context, p contract.Params) error {
			return deploy.CheckRepo(d, *p.(*contract.SiteRepoParams))
		}
	}
	jobHandlers["site.repo"] = siteRepo
}

// siteRepo connects a site to its GitHub repository and branch, with the
// site's own deploy key (R02-D3, R02-D4, R02-D7). It checks again first:
// the site may have been destroyed since the job was accepted.
func siteRepo(ctx context.Context, j *jobs.Context, p contract.Params) (any, error) {
	params := *p.(*contract.SiteRepoParams)
	d := j.Deps()
	if err := deploy.CheckRepo(d, params); err != nil {
		return nil, err
	}
	return deploy.Repo(ctx, d, j.Helper, helper.LookupAccount, params)
}
