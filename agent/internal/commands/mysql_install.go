package commands

import (
	"context"

	"zimam/agent/contract"
	"zimam/agent/internal/jobs"
	"zimam/agent/internal/mysql"
)

func init() {
	jobHandlers["mysql.install"] = mysqlInstall
}

// mysqlInstall installs MySQL from Ubuntu's archive. It has no precheck:
// running it again converges, so a server that has MySQL is no conflict.
func mysqlInstall(ctx context.Context, j *jobs.Context, _ contract.Params) (any, error) {
	version, err := mysql.Install(ctx, j.Deps())
	if err != nil {
		return nil, err
	}
	return contract.ServiceInstallResult{Version: version}, nil
}
