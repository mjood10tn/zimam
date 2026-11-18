package commands

import (
	"context"
	"fmt"
	"time"

	"zimam/agent/contract"
	"zimam/agent/internal/jobs"
	"zimam/agent/internal/node"
	"zimam/agent/internal/sysexec"
)

func init() {
	jobPrechecks["node.install"] = nodeInstallPrecheck
	jobHandlers["node.install"] = nodeInstall
}

// nodeInstallPrecheck refuses a major past its end date (R04-D6), and a
// switch of major while a site job runs. Unlike php.install it accepts an
// installed major: that is Update (D19).
func nodeInstallPrecheck(d sysexec.Deps) jobs.Precheck {
	return func(ctx context.Context, p contract.Params) error {
		major := p.(*contract.NodeInstallParams).Chosen()
		r, _ := major.Release() // the params passed Validate
		now := time.Now()
		if d.Now != nil {
			now = d.Now()
		}
		if !r.SupportedOn(now) {
			return contract.FieldErrors{"major": fmt.Sprintf("Node %s's support ended on %s, so it can't be installed", major, r.EndOfLife)}.Err()
		}
		return checkNodeSwitch(ctx, d, major)
	}
}

// checkNodeSwitch refuses, like node.uninstall, while a site job runs when
// another major is installed: the switch purges nodejs first.
func checkNodeSwitch(ctx context.Context, d sysexec.Deps, major contract.NodeMajor) error {
	have, err := node.Installed(ctx, d)
	if err != nil {
		return err
	}
	if have == "" || node.MajorOf(have) == major {
		return nil
	}
	return checkNoSiteJob(d)
}

// nodeInstall checks the switch again, since a site job may have started
// since the job was accepted.
func nodeInstall(ctx context.Context, j *jobs.Context, p contract.Params) (any, error) {
	d, major := j.Deps(), p.(*contract.NodeInstallParams).Chosen()
	if err := checkNodeSwitch(ctx, d, major); err != nil {
		return nil, err
	}
	return node.Install(ctx, d, major)
}
