package commands

import (
	"context"
	"fmt"
	"slices"

	"zimam/agent/contract"
	"zimam/agent/internal/jobs"
	"zimam/agent/internal/siteprocs"
	"zimam/agent/internal/sites"
	"zimam/agent/internal/sysexec"
)

func init() {
	jobPrechecks["site.workers.restart"] = func(d sysexec.Deps) jobs.Precheck {
		return func(_ context.Context, p contract.Params) error {
			_, err := restartable(d, *p.(*contract.SiteWorkersRestartParams))
			return err
		}
	}
	jobHandlers["site.workers.restart"] = siteWorkersRestart
}

// restartable is the site's state narrowed to the workers to restart: the
// one named, or all. A worker the state doesn't list is not_found.
func restartable(d sysexec.Deps, p contract.SiteWorkersRestartParams) (sites.State, error) {
	st, err := workerSite(d, p.Name)
	if err != nil || p.Worker == "" {
		return st, err
	}
	i := slices.IndexFunc(st.Workers, func(w sites.WorkerState) bool { return w.ID == p.Worker })
	if i < 0 {
		return sites.State{}, &contract.AgentError{Code: contract.ErrNotFound, Message: fmt.Sprintf("Site %s has no worker %s.", p.Name, p.Worker)}
	}
	st.Workers = st.Workers[i : i+1]
	return st, nil
}

// siteWorkersRestart restarts the site's workers, or one of them, gracefully
// (R04-D4). It checks again first: the worker may be gone since.
func siteWorkersRestart(ctx context.Context, j *jobs.Context, p contract.Params) (any, error) {
	params := *p.(*contract.SiteWorkersRestartParams)
	st, err := restartable(j.Deps(), params)
	if err != nil {
		return nil, err
	}
	if err := siteprocs.RestartWorkers(ctx, j.Deps(), st); err != nil {
		return nil, err
	}
	return contract.SiteNameResult{Name: params.Name}, nil
}
