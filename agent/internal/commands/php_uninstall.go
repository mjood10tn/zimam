package commands

import (
	"context"
	"fmt"

	"zimam/agent/contract"
	"zimam/agent/internal/config"
	"zimam/agent/internal/jobs"
	"zimam/agent/internal/php"
	"zimam/agent/internal/sysexec"
)

func init() {
	jobPrechecks["php.uninstall"] = func(d sysexec.Deps) jobs.Precheck {
		return func(_ context.Context, p contract.Params) error {
			return checkUninstall(d, p.(*contract.PHPVersionParams).Version)
		}
	}
	jobHandlers["php.uninstall"] = phpUninstall
}

// checkUninstall refuses a version that isn't installed, and one that the
// default, the panel or a site uses (§8.4). A site or the panel uses a
// version while it has a Zimam pool on it. agent.json is read on every
// call, since php.default changes it while serve runs.
func checkUninstall(d sysexec.Deps, v contract.PHPVersion) error {
	installed, err := php.Installed(d, v)
	if err != nil {
		return err
	}
	if !installed {
		return &contract.AgentError{Code: contract.ErrNotFound, Message: fmt.Sprintf("PHP %s isn't installed.", v)}
	}
	cfg, err := config.Load(d.Paths)
	if err != nil {
		return err
	}
	conflict := func(format string) error {
		return &contract.AgentError{Code: contract.ErrConflict, Message: fmt.Sprintf(format, v)}
	}
	switch {
	case v == cfg.PanelPHP:
		return conflict("The panel runs on PHP %s, so it can't be uninstalled.")
	case v == cfg.DefaultPHP:
		return conflict("PHP %s is the default version. Make another version the default first.")
	}
	pools, err := php.HasPools(d, v)
	if err != nil {
		return err
	}
	if pools {
		return conflict("Sites still run on PHP %s. Move them to another version or delete them first.")
	}
	return nil
}

// phpUninstall checks again, since a site may have got a pool on this
// version since the job was accepted, then removes it.
func phpUninstall(ctx context.Context, j *jobs.Context, p contract.Params) (any, error) {
	v := p.(*contract.PHPVersionParams).Version
	d := j.Deps()
	if err := checkUninstall(d, v); err != nil {
		return nil, err
	}
	if err := php.Uninstall(ctx, d, v); err != nil {
		return nil, err
	}
	return contract.PHPVersionResult{Version: v}, nil
}
