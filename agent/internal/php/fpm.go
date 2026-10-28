package php

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"

	"zimam/agent/contract"
	"zimam/agent/internal/configtx"
	"zimam/agent/internal/sysexec"
)

const systemctl = "/usr/bin/systemctl"

// HasPools reports whether v has a Zimam pool, a site's or the panel's:
// a zimam-*.conf file in its pool.d.
func HasPools(d sysexec.Deps, v contract.PHPVersion) (bool, error) {
	if err := v.Validate(); err != nil {
		return false, fmt.Errorf("PHP %q %v", v, err)
	}
	entries, err := os.ReadDir(d.Paths.PHPPoolDir(v))
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	for _, e := range entries {
		if e.Type().IsRegular() && strings.HasPrefix(e.Name(), "zimam-") && strings.HasSuffix(e.Name(), ".conf") {
			return true, nil
		}
	}
	return false, nil
}

// ApplyFPM brings v's FPM service in line with its pools (§8.3 step 6).
// FPM refuses to start without a pool, so with none the service is stopped
// and disabled. Otherwise the config is tested, then the service is
// reloaded if it runs, or enabled and started if it doesn't.
func ApplyFPM(ctx context.Context, d sysexec.Deps, v contract.PHPVersion) error {
	pools, err := HasPools(d, v)
	if err != nil {
		return err
	}
	unit := "php" + string(v) + "-fpm"
	if !pools {
		d.Logf("PHP %s has no pool: stopping and disabling %s", v, unit)
		_, err := d.Run.Run(ctx, sysexec.Cmd{Path: systemctl, Args: []string{"disable", "--now", unit}})
		return err
	}
	return configtx.WithReloadLock(ctx, d.Paths, func() error {
		if err := testFPM(ctx, d, v); err != nil {
			return err
		}
		_, err := d.Run.Run(ctx, sysexec.Cmd{Path: systemctl, Args: []string{"is-active", "--quiet", unit}})
		var exit *sysexec.ExitError
		switch {
		case err == nil:
			d.Logf("Reloading %s", unit)
			_, err = d.Run.Run(ctx, sysexec.Cmd{Path: systemctl, Args: []string{"reload", unit}})
		case errors.As(err, &exit):
			d.Logf("Starting %s", unit)
			_, err = d.Run.Run(ctx, sysexec.Cmd{Path: systemctl, Args: []string{"enable", "--now", unit}})
		}
		return err
	})
}

// testFPM runs php-fpm<v> -t. A failure carries its output.
func testFPM(ctx context.Context, d sysexec.Deps, v contract.PHPVersion) error {
	return configtx.RunTest(ctx, d.Run, sysexec.Cmd{Path: "/usr/sbin/php-fpm" + string(v), Args: []string{"-t"}})
}
