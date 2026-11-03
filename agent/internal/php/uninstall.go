package php

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"slices"

	"zimam/agent/contract"
	"zimam/agent/internal/sysexec"
)

const dpkg = "/usr/bin/dpkg"

// dpkgInterrupted is how apt-get refuses to work after a dpkg run was cut
// short (checked on Ubuntu 26.04).
var dpkgInterrupted = []byte("E: dpkg was interrupted")

// Installed reports whether v is installed: its php-fpm binary exists, as
// system.status counts it.
func Installed(d sysexec.Deps, v contract.PHPVersion) (bool, error) {
	_, err := os.Stat(d.Paths.PHPFPMBinary(v))
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, fs.ErrNotExist):
		return false, nil
	default:
		return false, err
	}
}

// Uninstall removes PHP v (§8.4): it stops and disables v's FPM service,
// then purges v's packages. The caller checks first that nothing uses v.
// Zimam's own ini and [global] files stay in /etc/php/<v>; without the
// packages nothing reads them, and installing v again rewrites them.
func Uninstall(ctx context.Context, d sysexec.Deps, v contract.PHPVersion) error {
	if err := v.Validate(); err != nil {
		return fmt.Errorf("PHP %q %v", v, err)
	}
	unit := "php" + string(v) + "-fpm"
	d.Logf("Stopping and disabling %s", unit)
	if _, err := d.Run.Run(ctx, sysexec.Cmd{Path: systemctl, Args: []string{"disable", "--now", unit}}); err != nil {
		return err
	}
	d.Logf("Purging the PHP %s packages", v)
	args := append(slices.Clone(aptLockWait), "purge", "-y")
	return aptGetRepairing(ctx, d, append(args, Packages(v)...))
}

// aptGetRepairing runs apt-get with args. If apt-get refuses because a dpkg
// run was cut short, by a job that hit its time or memory limit say, it
// finishes that run with dpkg --configure -a and tries once more. apt-get
// checks for that while holding dpkg's lock, so a dpkg run that is merely
// in progress elsewhere never looks interrupted.
func aptGetRepairing(ctx context.Context, d sysexec.Deps, args []string) error {
	cmd := sysexec.Cmd{Path: aptGet, Args: args, Env: aptEnv}
	_, err := d.Run.Run(ctx, cmd)
	var exit *sysexec.ExitError
	if !errors.As(err, &exit) || !bytes.Contains(exit.Stderr, dpkgInterrupted) {
		return err
	}
	d.Logf("A dpkg run was interrupted; finishing it with dpkg --configure -a")
	if _, err := d.Run.Run(ctx, sysexec.Cmd{Path: dpkg, Args: []string{"--force-confdef", "--force-confold", "--configure", "-a"}, Env: aptEnv}); err != nil {
		return err
	}
	_, err = d.Run.Run(ctx, cmd)
	return err
}
