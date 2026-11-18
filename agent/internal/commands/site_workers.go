package commands

import (
	"context"
	"errors"
	"fmt"
	"io/fs"

	"zimam/agent/contract"
	"zimam/agent/internal/helper"
	"zimam/agent/internal/jobs"
	"zimam/agent/internal/siteprocs"
	"zimam/agent/internal/sites"
	"zimam/agent/internal/sysexec"
)

func init() {
	jobPrechecks["site.workers"] = func(d sysexec.Deps) jobs.Precheck {
		return func(_ context.Context, p contract.Params) error {
			_, err := workerSite(d, p.(*contract.SiteWorkersParams).Name)
			return err
		}
	}
	jobHandlers["site.workers"] = siteWorkers
}

// loadSite is the state of a site, not_found for one that doesn't exist.
func loadSite(d sysexec.Deps, name contract.SiteName) (sites.State, error) {
	st, err := sites.Load(d.Paths, name)
	if errors.Is(err, fs.ErrNotExist) {
		return sites.State{}, &contract.AgentError{Code: contract.ErrNotFound, Message: fmt.Sprintf("Site %s doesn't exist.", name)}
	}
	return st, err
}

// workerSite is the state of a site that can have workers: not_found for a
// site that doesn't exist, conflict for a static one, which has no PHP to
// run them (as site.php answers it).
func workerSite(d sysexec.Deps, name contract.SiteName) (sites.State, error) {
	st, err := loadSite(d, name)
	if err != nil {
		return sites.State{}, err
	}
	if !st.Type.RunsPHP() {
		return sites.State{}, conflict("Site %s is static, so it has no PHP to run workers.", name)
	}
	return st, nil
}

// siteWorkers makes the site's worker units and scripts match the list
// (R04-D3). It checks again first: the site may be gone since the job was
// accepted.
func siteWorkers(ctx context.Context, j *jobs.Context, p contract.Params) (any, error) {
	params := p.(*contract.SiteWorkersParams)
	d := j.Deps()
	st, err := workerSite(d, params.Name)
	if err != nil {
		return nil, err
	}
	a, err := helper.LookupAccount("s_" + string(params.Name))
	if err != nil {
		return nil, err
	}
	if _, err := siteprocs.Reconcile(ctx, d, j.Helper, a, st, params.Workers); err != nil {
		return nil, err
	}
	return contract.SiteNameResult{Name: params.Name}, nil
}
