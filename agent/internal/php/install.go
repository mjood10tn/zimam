package php

import (
	"context"
	"fmt"
	"slices"

	"zimam/agent/contract"
	"zimam/agent/internal/configtx"
	"zimam/agent/internal/sysexec"
	"zimam/agent/internal/templates"
)

// Install installs PHP v (§8.3 steps 1–6): the repository, the packages,
// Zimam's ini file and global FPM settings, without the stock www.conf pool.
// Then it starts, reloads or stops v's FPM service to match its pools.
// Running it again converges, and finishes a dpkg run that a killed job
// interrupted.
func Install(ctx context.Context, d sysexec.Deps, v contract.PHPVersion) error {
	if err := v.Validate(); err != nil {
		return fmt.Errorf("PHP %q %v", v, err)
	}
	if err := EnsureRepo(ctx, d); err != nil {
		return err
	}
	d.Logf("Installing the PHP %s packages", v)
	// Never stop at a dpkg question about a changed config file: there is
	// nobody to answer it.
	args := append(slices.Clone(aptLockWait), "-o", "Dpkg::Options::=--force-confdef", "-o", "Dpkg::Options::=--force-confold",
		"install", "-y", "--no-install-recommends")
	if err := aptGetRepairing(ctx, d, append(args, Packages(v)...)); err != nil {
		return err
	}
	ini, err := templates.Render("php/99-zimam.ini", nil)
	if err != nil {
		return err
	}
	global, err := templates.Render("php/00-zimam-global.conf", nil)
	if err != nil {
		return err
	}
	pools, err := HasPools(d, v)
	if err != nil {
		return err
	}
	d.Logf("Writing PHP %s's baseline settings and removing its stock www pool", v)
	err = configtx.WithReloadLock(ctx, d.Paths, func() error {
		return configtx.Apply(ctx, []configtx.Change{
			configtx.RootFile(d.Paths.PHPIni(v), ini, 0o644),
			configtx.RootFile(d.Paths.PHPGlobalConf(v), global, 0o644),
			{Path: d.Paths.PHPWwwPool(v), Remove: true},
		}, func(ctx context.Context) error {
			// php-fpm -t fails without a pool; the first pool's own
			// transaction tests these files then.
			if !pools {
				return nil
			}
			return testFPM(ctx, d, v)
		})
	})
	if err != nil {
		return err
	}
	return ApplyFPM(ctx, d, v)
}
