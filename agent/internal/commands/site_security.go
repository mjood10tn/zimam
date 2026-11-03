package commands

import (
	"context"

	"zimam/agent/contract"
	"zimam/agent/internal/jobs"
	"zimam/agent/internal/sites"
	"zimam/agent/internal/sysexec"
)

func init() {
	jobPrechecks["site.security"] = func(d sysexec.Deps) jobs.Precheck {
		return func(_ context.Context, p contract.Params) error {
			return sites.CheckSecurity(d, *p.(*contract.SiteSecurityParams))
		}
	}
	jobHandlers["site.security"] = siteSecurity
}

// siteSecurity applies a site's whole set of security settings (§9.6). It
// checks again first: the certificate force_https needs may be gone.
func siteSecurity(ctx context.Context, j *jobs.Context, p contract.Params) (any, error) {
	return sites.ApplySecurity(ctx, j.Deps(), *p.(*contract.SiteSecurityParams))
}
