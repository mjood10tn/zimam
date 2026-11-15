package php

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"zimam/agent/contract"
	"zimam/agent/internal/configtx"
	"zimam/agent/internal/paths"
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

// MarkReload marks v for a graceful reload by the next maintenance run
// (spike amendment A3): a deploy's new code is served at once, since
// OPcache keys on the real path, so the reload only lets old releases'
// scripts leave OPcache, and a reload per deploy would stall every pool of
// v. The mark is an empty file, root 0600 in a root 0700 directory.
func MarkReload(p paths.Paths, v contract.PHPVersion) error {
	if err := v.Validate(); err != nil {
		return fmt.Errorf("PHP %q %v", v, err)
	}
	dir := p.FPMReloadDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(p.FPMReloadMark(v), os.O_WRONLY|os.O_CREATE|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return err
	}
	return f.Close()
}

// ReloadMarked reloads each PHP version a deploy marked (MarkReload) and
// clears its mark; it returns the versions it reloaded. The mark goes
// before the reload, so a deploy that marks again meanwhile keeps its own,
// and comes back when the reload fails, for the next run. A version with
// nothing to reload (not installed, no pool, its service not running) loses
// its mark, as does anything in the directory that isn't a supported
// version's file.
func ReloadMarked(ctx context.Context, d sysexec.Deps) ([]contract.PHPVersion, error) {
	dir := d.Paths.FPMReloadDir()
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var reloaded []contract.PHPVersion
	var errs []error
	for _, e := range entries {
		v := contract.PHPVersion(e.Name())
		if v.Validate() != nil || !e.Type().IsRegular() {
			d.Logf("Removing %s/%s, which is no PHP version's reload mark", dir, e.Name())
			if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil {
				errs = append(errs, err)
			}
			continue
		}
		if err := os.Remove(d.Paths.FPMReloadMark(v)); err != nil {
			errs = append(errs, err)
			continue
		}
		did, err := reloadIfRunning(ctx, d, v)
		if err != nil {
			errs = append(errs, fmt.Errorf("reloading PHP %s: %w", v, err))
			if err := MarkReload(d.Paths, v); err != nil {
				errs = append(errs, err)
			}
			continue
		}
		if did {
			reloaded = append(reloaded, v)
		}
	}
	return reloaded, errors.Join(errs...)
}

// reloadIfRunning tests v's config and reloads its FPM service gracefully
// (§8.3), under the reload lock, when v is installed, has a pool and its
// service runs. It reports whether it reloaded.
func reloadIfRunning(ctx context.Context, d sysexec.Deps, v contract.PHPVersion) (bool, error) {
	installed, err := Installed(d, v)
	if err != nil || !installed {
		return false, err
	}
	pools, err := HasPools(d, v)
	if err != nil || !pools {
		return false, err
	}
	unit := "php" + string(v) + "-fpm"
	reloaded := false
	err = configtx.WithReloadLock(ctx, d.Paths, func() error {
		_, err := d.Run.Run(ctx, sysexec.Cmd{Path: systemctl, Args: []string{"is-active", "--quiet", unit}})
		var exit *sysexec.ExitError
		if errors.As(err, &exit) {
			return nil // not running: nothing to reload
		}
		if err != nil {
			return err
		}
		if err := testFPM(ctx, d, v); err != nil {
			return err
		}
		d.Logf("Reloading %s, so old releases' scripts leave OPcache", unit)
		if _, err := d.Run.Run(ctx, sysexec.Cmd{Path: systemctl, Args: []string{"reload", unit}}); err != nil {
			return err
		}
		reloaded = true
		return nil
	})
	return reloaded, err
}
