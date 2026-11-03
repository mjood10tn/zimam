package commands

import (
	"context"

	"zimam/agent/contract"
	"zimam/agent/internal/jobs"
	"zimam/agent/internal/sites"
	"zimam/agent/internal/sysexec"
)

func init() {
	jobPrechecks["site.php"] = func(d sysexec.Deps) jobs.Precheck {
		return func(_ context.Context, p contract.Params) error {
			return sites.CheckSwitch(d, *p.(*contract.SitePHPParams))
		}
	}
	jobHandlers["site.php"] = sitePHP
}

// sitePHP checks again, since the version may have been uninstalled since
// the job was accepted, then switches the site (§9.5).
func sitePHP(ctx context.Context, j *jobs.Context, p contract.Params) (any, error) {
	params := *p.(*contract.SitePHPParams)
	d := j.Deps()
	if err := sites.CheckSwitch(d, params); err != nil {
		return nil, err
	}
	return sites.Switch(ctx, d, params)
}
