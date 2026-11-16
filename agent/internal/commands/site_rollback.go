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
	jobPrechecks["site.rollback"] = func(d sysexec.Deps) jobs.Precheck {
		return func(_ context.Context, p contract.Params) error {
			return deploy.CheckRollback(d, *p.(*contract.SiteRollbackParams))
		}
	}
	jobHandlers["site.rollback"] = siteRollback
}

// siteRollback makes a kept release live again. deploy.Rollback checks
// again first: a deploy may have pruned the release since the job was
// accepted.
func siteRollback(ctx context.Context, j *jobs.Context, p contract.Params) (any, error) {
	res, err := deploy.Rollback(ctx, j.Deps(), j.Helper, helper.LookupAccount, *p.(*contract.SiteRollbackParams))
	if err != nil && res.Release != "" {
		// As for a deploy: the release is live, and the result says so.
		return res, &jobs.Landed{Err: err}
	}
	return res, err
}
