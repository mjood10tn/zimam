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
	jobPrechecks["site.deploy"] = func(d sysexec.Deps) jobs.Precheck {
		return func(_ context.Context, p contract.Params) error {
			return deploy.CheckDeploy(d, *p.(*contract.SiteDeployParams))
		}
	}
	jobHandlers["site.deploy"] = siteDeploy
}

// siteDeploy builds a release of the site's branch and makes it live
// (R02-D5). deploy.Run checks again first: the site may have been
// destroyed, or lost its repository, since the job was accepted.
func siteDeploy(ctx context.Context, j *jobs.Context, p contract.Params) (any, error) {
	return deploy.Run(ctx, j.Deps(), j.Helper, helper.LookupAccount, *p.(*contract.SiteDeployParams))
}
