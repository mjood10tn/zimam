package commands

import (
	"context"

	"zimam/agent/contract"
	"zimam/agent/internal/jobs"
	"zimam/agent/internal/sites"
	"zimam/agent/internal/sysexec"
)

func init() {
	jobPrechecks["cert.issue"] = func(d sysexec.Deps) jobs.Precheck {
		return func(_ context.Context, p contract.Params) error {
			return sites.CheckCertificate(d, p.(*contract.SiteNameParams).Name)
		}
	}
	jobHandlers["cert.issue"] = certIssue
}

// certIssue checks again, since the site may have been deleted since the
// job was accepted, then gets its certificate and turns on HTTPS (§10).
func certIssue(ctx context.Context, j *jobs.Context, p contract.Params) (any, error) {
	name := p.(*contract.SiteNameParams).Name
	d := j.Deps()
	if err := sites.CheckCertificate(d, name); err != nil {
		return nil, err
	}
	return sites.IssueCertificate(ctx, d, name)
}
