package commands

import (
	"context"
	"errors"
	"fmt"
	"io/fs"

	"zimam/agent/contract"
	"zimam/agent/internal/checks"
	"zimam/agent/internal/helper"
	"zimam/agent/internal/jobs"
	"zimam/agent/internal/sites"
	"zimam/agent/internal/sysexec"
)

func init() {
	jobPrechecks["security.check"] = func(d sysexec.Deps) jobs.Precheck {
		return func(_ context.Context, p contract.Params) error {
			return checkSecurityTarget(d, *p.(*contract.SecurityCheckParams))
		}
	}
	jobHandlers["security.check"] = securityCheck
}

// checkSecurityTarget answers not_found for a site without a state file.
func checkSecurityTarget(d sysexec.Deps, p contract.SecurityCheckParams) error {
	if p.Scope != contract.CheckScopeSite {
		return nil
	}
	_, err := sites.Load(d.Paths, p.Name)
	if errors.Is(err, fs.ErrNotExist) {
		return &contract.AgentError{Code: contract.ErrNotFound, Message: fmt.Sprintf("Site %s doesn't exist.", p.Name)}
	}
	return err
}

// securityCheck runs the server's or one site's checks (§9.7). It is a
// job, audited like every other, because the dotfile check writes a canary
// into the site through the helper.
func securityCheck(ctx context.Context, j *jobs.Context, p contract.Params) (any, error) {
	params := *p.(*contract.SecurityCheckParams)
	e := checks.Env{D: j.Deps(), Helper: j.Helper, Lookup: helper.LookupAccount, Probe: checks.DefaultProbe}
	var (
		findings []contract.Finding
		err      error
	)
	if params.Scope == contract.CheckScopeSite {
		findings, err = checks.Site(ctx, e, params.Name)
	} else {
		findings, err = checks.Server(ctx, e)
	}
	if err != nil {
		return nil, err
	}
	for _, f := range findings {
		j.Logf("%s: %s. %s", f.Check, f.Status, f.Detail)
	}
	return contract.SecurityCheckResult{Scope: params.Scope, Name: params.Name, Findings: findings}, nil
}
