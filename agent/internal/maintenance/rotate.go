package maintenance

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"time"

	"zimam/agent/internal/sysexec"
)

// perFileBudget is how long logrotate may take over one rules file. A real
// 200 MB log compresses in seconds; a tenant's huge sparse log must not hold
// up the other sites.
var perFileBudget = 3 * time.Minute

// rotationStep is the context of the whole rotation step: the run's, ending
// limits.rotationReserve early, so the steps after it still have time.
func rotationStep(ctx context.Context) (context.Context, context.CancelFunc) {
	if dl, ok := ctx.Deadline(); ok {
		return context.WithDeadline(ctx, dl.Add(-limits.rotationReserve))
	}
	return context.WithCancel(ctx)
}

// rotateEach runs logrotate once per rules file, with Zimam's state file,
// never Ubuntu's (§11.3): zimam-own.conf first, then each site-<name>.conf
// in name order. Each file's rules stand alone, so one run needs nothing
// from the others. A run that passes perFile is killed with its children,
// logged and returned in timedOut, and the next file still runs. A file
// whose run fails otherwise is reported in err and doesn't stop the rest;
// a cancelled ctx does.
func rotateEach(ctx context.Context, d sysexec.Deps, perFile time.Duration) (timedOut []string, err error) {
	sites, err := filepath.Glob(filepath.Join(d.Paths.LogrotateDir(), "site-*.conf"))
	if err != nil {
		return nil, fmt.Errorf("listing the sites' rotation rules: %w", err)
	}
	slices.Sort(sites)
	var errs []error
	for _, file := range append([]string{d.Paths.OwnLogrotateConf()}, sites...) {
		if err := ctx.Err(); err != nil {
			errs = append(errs, fmt.Errorf("rotating logs: %w", err))
			break
		}
		fctx, cancel := context.WithTimeout(ctx, perFile)
		_, err := d.Run.Run(fctx, sysexec.Cmd{
			Path:      logrotate,
			Args:      []string{"--state", d.Paths.LogrotateState(), file},
			KillGroup: true,
		})
		cancel()
		switch {
		case err == nil:
		case ctx.Err() == nil && errors.Is(err, context.DeadlineExceeded):
			d.Logf("Rotating %s took longer than %s; the next file continues", file, perFile)
			timedOut = append(timedOut, file)
		case ctx.Err() != nil:
			errs = append(errs, fmt.Errorf("rotating logs: %w", ctx.Err()))
		default:
			errs = append(errs, fmt.Errorf("rotating logs over %s: %w", file, err))
		}
	}
	return timedOut, errors.Join(errs...)
}
