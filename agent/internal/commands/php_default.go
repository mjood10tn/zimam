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
	jobPrechecks["php.default"] = func(d sysexec.Deps) jobs.Precheck {
		return func(_ context.Context, p contract.Params) error {
			return checkDefault(d, p.(*contract.PHPVersionParams).Version)
		}
	}
	jobHandlers["php.default"] = phpDefault
}

// checkDefault refuses a version that isn't installed: update-alternatives
// can't point php at a binary that doesn't exist.
func checkDefault(d sysexec.Deps, v contract.PHPVersion) error {
	installed, err := php.Installed(d, v)
	if err != nil {
		return err
	}
	if !installed {
		return &contract.AgentError{Code: contract.ErrNotFound, Message: fmt.Sprintf("PHP %s isn't installed.", v)}
	}
	return nil
}

// phpDefault makes v what `php` runs and stores it as default_php (§8.5).
// Pools and vhosts stay as they are, so existing sites don't change. Its
// subject is server, like logs.policy's, so no other job writes agent.json
// meanwhile.
func phpDefault(ctx context.Context, j *jobs.Context, p contract.Params) (any, error) {
	v := p.(*contract.PHPVersionParams).Version
	d := j.Deps()
	if err := checkDefault(d, v); err != nil {
		return nil, err
	}
	if err := php.SetDefault(ctx, d, v); err != nil {
		return nil, err
	}
	cfg, err := config.Load(d.Paths)
	if err != nil {
		return nil, err
	}
	cfg.DefaultPHP = v
	if err := config.Save(d.Paths, cfg); err != nil {
		return nil, err
	}
	j.Logf("PHP %s is now the default", v)
	return contract.PHPVersionResult{Version: v}, nil
}
