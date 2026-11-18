package commands

import (
	"context"

	"zimam/agent/contract"
	"zimam/agent/internal/helper"
	"zimam/agent/internal/jobs"
	"zimam/agent/internal/siteprocs"
	"zimam/agent/internal/sites"
	"zimam/agent/internal/sysexec"
)

func init() {
	jobPrechecks["site.cron"] = func(d sysexec.Deps) jobs.Precheck {
		return func(_ context.Context, p contract.Params) error {
			_, err := cronSite(d, p.(*contract.SiteCronParams))
			return err
		}
	}
	jobHandlers["site.cron"] = siteCron
}

// cronSite is the state of a site that can have these cron jobs: not_found
// for a site that doesn't exist. A static site may have jobs, which run
// anything, but not the scheduler: that is artisan, and it has no PHP (conflict).
func cronSite(d sysexec.Deps, p *contract.SiteCronParams) (sites.State, error) {
	st, err := loadSite(d, p.Name)
	if err != nil {
		return sites.State{}, err
	}
	if p.Scheduler && !st.Type.RunsPHP() {
		return sites.State{}, conflict("Site %s is static, so it has no Laravel scheduler to run.", p.Name)
	}
	return st, nil
}

// siteCron makes the site's cron file and scripts match the list (R04-D5).
// It checks again first: the site may be gone since the job was accepted.
func siteCron(ctx context.Context, j *jobs.Context, p contract.Params) (any, error) {
	params := p.(*contract.SiteCronParams)
	d := j.Deps()
	st, err := cronSite(d, params)
	if err != nil {
		return nil, err
	}
	a, err := helper.LookupAccount("s_" + string(params.Name))
	if err != nil {
		return nil, err
	}
	if _, err := siteprocs.ReconcileCron(ctx, d, j.Helper, a, st, *params); err != nil {
		return nil, err
	}
	return contract.SiteNameResult{Name: params.Name}, nil
}
