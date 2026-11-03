package jobs

import (
	"context"
	"slices"

	"zimam/agent/internal/sysexec"
)

// packageGuard runs apt-get and dpkg under the job's cancel lock, so
// job.cancel refuses rather than stopping dpkg midway: systemctl stop
// signals everything in the unit, and an interrupted dpkg leaves the
// package database needing dpkg --configure -a. apt-get update changes no
// package, so it stays cancellable.
type packageGuard struct {
	sysexec.Runner
	lock string
}

func (g packageGuard) Run(ctx context.Context, c sysexec.Cmd) (sysexec.Result, error) {
	if !changesPackages(c) {
		return g.Runner.Run(ctx, c)
	}
	unlock, err := sysexec.Lock(ctx, g.lock)
	if err != nil {
		return sysexec.Result{}, err
	}
	defer unlock()
	return g.Runner.Run(ctx, c)
}

func changesPackages(c sysexec.Cmd) bool {
	switch c.Path {
	case "/usr/bin/dpkg":
		return true
	case "/usr/bin/apt-get":
		return !slices.Contains(c.Args, "update")
	}
	return false
}
