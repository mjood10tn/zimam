package commands

import (
	"context"
	"errors"
	"fmt"
	"io/fs"

	"zimam/agent/contract"
	"zimam/agent/internal/siteprocs"
	"zimam/agent/internal/sites"
	"zimam/agent/internal/sysexec"
)

// siteWorkersStatus answers site.workers.status: each worker of the site's
// state with its processes as systemd reports them. A site without workers,
// a static one included, has an empty list.
func siteWorkersStatus(d sysexec.Deps) Handler {
	return func(ctx context.Context, p contract.Params) (any, error) {
		name := p.(*contract.SiteWorkersStatusParams).Name
		st, err := sites.Load(d.Paths, name)
		if errors.Is(err, fs.ErrNotExist) {
			return nil, &contract.AgentError{Code: contract.ErrNotFound, Message: fmt.Sprintf("Site %s doesn't exist.", name)}
		}
		if err != nil {
			return nil, err
		}
		return siteprocs.Status(ctx, d, st)
	}
}
