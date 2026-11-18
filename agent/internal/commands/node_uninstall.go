package commands

import (
	"context"
	"strings"

	"zimam/agent/contract"
	"zimam/agent/internal/jobs"
	"zimam/agent/internal/node"
	"zimam/agent/internal/sysexec"
)

func init() {
	jobPrechecks["node.uninstall"] = func(d sysexec.Deps) jobs.Precheck {
		return func(ctx context.Context, _ contract.Params) error { return checkNodeUninstall(ctx, d) }
	}
	jobHandlers["node.uninstall"] = nodeUninstall
}

// checkNodeUninstall refuses when Node.js is not installed, and while any
// site job (a deploy runs npm) is queued or running (R04-D6). Nothing else
// tells whether a site uses Node, since a site's build is its own.
func checkNodeUninstall(ctx context.Context, d sysexec.Deps) error {
	version, err := node.Installed(ctx, d)
	if err != nil {
		return err
	}
	if version == "" {
		return &contract.AgentError{Code: contract.ErrNotFound, Message: "Node.js isn't installed."}
	}
	subjects, err := jobs.ActiveSubjects(d.Paths)
	if err != nil {
		return err
	}
	for _, s := range subjects {
		if strings.HasPrefix(s, "site:") {
			return &contract.AgentError{Code: contract.ErrBusy, Message: "A site job is running and may be using Node.js. Try again when it has finished."}
		}
	}
	return nil
}

// nodeUninstall checks again, since a site job may have started since the
// job was accepted, then removes Node.js.
func nodeUninstall(ctx context.Context, j *jobs.Context, _ contract.Params) (any, error) {
	d := j.Deps()
	if err := checkNodeUninstall(ctx, d); err != nil {
		return nil, err
	}
	if err := node.Uninstall(ctx, d); err != nil {
		return nil, err
	}
	return contract.NodeUninstallResult{}, nil
}
