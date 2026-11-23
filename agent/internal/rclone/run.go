package rclone

import (
	"context"
	"runtime"

	"zimam/agent/internal/sysexec"
)

// Run runs the pinned rclone, its hash checked first, with args after
// Zimam's own flags. The config is always rclone.conf, and the environment
// is sysexec's base alone: no RCLONE_* variable or HOME can configure it, and
// secrets reach it only from the config file, never argv (global rule 6).
func Run(ctx context.Context, d sysexec.Deps, args ...string) (sysexec.Result, error) {
	r, err := pinned(runtime.GOARCH)
	if err != nil {
		return sysexec.Result{}, err
	}
	return run(ctx, d, r, args...)
}

func run(ctx context.Context, d sysexec.Deps, r release, args ...string) (sysexec.Result, error) {
	bin, err := ensure(ctx, d, r)
	if err != nil {
		return sysexec.Result{}, err
	}
	return d.Run.Run(ctx, sysexec.Cmd{
		Path: bin,
		Args: append([]string{"--config", d.Paths.RcloneConf(), "--low-level-retries", "3"}, args...),
	})
}
