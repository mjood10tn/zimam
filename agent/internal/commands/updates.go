package commands

import (
	"context"

	"zimam/agent/contract"
	"zimam/agent/internal/jobs"
	"zimam/agent/internal/osupdates"
)

// The OS update commands (R06-D12..D16).
func init() {
	jobHandlers["updates.auto"] = func(ctx context.Context, j *jobs.Context, p contract.Params) (any, error) {
		return osupdates.Auto(ctx, j.Deps(), *p.(*contract.UpdatesAutoParams))
	}
	jobHandlers["updates.install"] = func(ctx context.Context, j *jobs.Context, p contract.Params) (any, error) {
		return osupdates.Install(ctx, j.Deps(), p.(*contract.UpdatesInstallParams).Scope)
	}
	jobHandlers["system.reboot"] = func(ctx context.Context, j *jobs.Context, p contract.Params) (any, error) {
		return osupdates.Reboot(ctx, j.Deps(), p.(*contract.SystemRebootParams).At)
	}
	jobHandlers["system.reboot.cancel"] = func(ctx context.Context, j *jobs.Context, _ contract.Params) (any, error) {
		return osupdates.Cancel(ctx, j.Deps())
	}
}
