package commands

import (
	"context"
	"fmt"
	"time"

	"zimam/agent/contract"
	"zimam/agent/internal/jobs"
	"zimam/agent/internal/php"
	"zimam/agent/internal/sysexec"
)

// jobPrechecks build the prechecks of the job commands that have one, from
// serve's view of the system. Each command's file adds its own from init.
var jobPrechecks = map[string]func(d sysexec.Deps) jobs.Precheck{}

// Prechecks returns the job commands' prechecks, which run before a job is
// accepted (§5.7).
func Prechecks(d sysexec.Deps) map[string]jobs.Precheck {
	out := make(map[string]jobs.Precheck, len(jobPrechecks))
	for name, build := range jobPrechecks {
		out[name] = build(d)
	}
	return out
}

func init() {
	jobPrechecks["php.install"] = phpInstallPrecheck
	jobHandlers["php.install"] = phpInstall
}

// phpInstallPrecheck refuses a version past its end of support (§8.1) and
// one that is already installed.
func phpInstallPrecheck(d sysexec.Deps) jobs.Precheck {
	return func(_ context.Context, p contract.Params) error {
		v := p.(*contract.PHPVersionParams).Version
		r, _ := v.Release() // the params passed Validate
		now := time.Now()
		if d.Now != nil {
			now = d.Now()
		}
		if !r.SupportedOn(now) {
			return contract.FieldErrors{"version": fmt.Sprintf("PHP %s's security support ended on %s, so it can't be installed", v, r.SecurityEnd)}.Err()
		}
		installed, err := php.Installed(d, v)
		if err != nil {
			return err
		}
		if installed {
			return &contract.AgentError{Code: contract.ErrConflict, Message: fmt.Sprintf("PHP %s is already installed.", v)}
		}
		return nil
	}
}

func phpInstall(ctx context.Context, j *jobs.Context, p contract.Params) (any, error) {
	v := p.(*contract.PHPVersionParams).Version
	if err := php.Install(ctx, j.Deps(), v); err != nil {
		return nil, err
	}
	return contract.PHPVersionResult{Version: v}, nil
}
