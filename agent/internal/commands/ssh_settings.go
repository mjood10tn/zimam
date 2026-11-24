package commands

import (
	"context"

	"zimam/agent/contract"
	"zimam/agent/internal/jobs"
	"zimam/agent/internal/sshd"
)

func init() { jobHandlers["ssh.settings"] = sshSettings }

// sshSettings writes the SSH settings as a guarded change (R06-D1, D4).
func sshSettings(ctx context.Context, j *jobs.Context, p contract.Params) (any, error) {
	id, deadline, err := sshd.Apply(ctx, j.Deps(), *p.(*contract.SSHSettingsParams))
	if err != nil {
		return nil, err
	}
	return contract.SSHSettingsResult{Change: id, Deadline: deadline}, nil
}
