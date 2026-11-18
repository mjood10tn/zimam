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

// nodeInstallPrecheck refuses a major past its end date (R04-D6). Unlike
// php.install it accepts an installed major: that is Update (D19).
func nodeInstallPrecheck(d sysexec.Deps) jobs.Precheck {
	return func(_ context.Context, p contract.Params) error {
		major := p.(*contract.NodeInstallParams).Chosen()
		r, _ := major.Release() // the params passed Validate
		now := time.Now()
		if d.Now != nil {
			now = d.Now()
		}
		if !r.SupportedOn(now) {
			return contract.FieldErrors{"major": fmt.Sprintf("Node %s's support ended on %s, so it can't be installed", major, r.EndOfLife)}.Err()
		}
		return nil
	}
}

func nodeInstall(ctx context.Context, j *jobs.Context, p contract.Params) (any, error) {
	return node.Install(ctx, j.Deps(), p.(*contract.NodeInstallParams).Chosen())
}
