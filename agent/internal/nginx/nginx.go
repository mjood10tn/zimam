// Package nginx tests and reloads Nginx, and applies a vhost with its
// sites-enabled link as one change that is undone if Nginx rejects it
// (design §5.5, §9.3).
package nginx

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"

	"zimam/agent/internal/configtx"
	"zimam/agent/internal/sysexec"
)

const (
	nginxBin  = "/usr/sbin/nginx"
	systemctl = "/usr/bin/systemctl"
)

// Link is a sites-enabled symlink: Path points at Target, the vhost in
// sites-available.
type Link struct{ Path, Target string }

// Test runs nginx -t. A failure is a *configtx.TestFailed with its output.
func Test(ctx context.Context, d sysexec.Deps) error {
	return configtx.RunTest(ctx, d.Run, sysexec.Cmd{Path: nginxBin, Args: []string{"-t"}})
}

// Reload reloads Nginx under the reload lock, or starts it if it isn't
// running.
func Reload(ctx context.Context, d sysexec.Deps) error {
	return configtx.WithReloadLock(ctx, d.Paths, func() error { return reload(ctx, d) })
}

func reload(ctx context.Context, d sysexec.Deps) error {
	d.Logf("Reloading Nginx")
	_, err := d.Run.Run(ctx, sysexec.Cmd{Path: systemctl, Args: []string{"reload-or-restart", "nginx"}})
	return err
}

// ApplySite writes changes and enables the site, tests Nginx and reloads
// it, all under the reload lock. If the test fails, the files come back as
// they were and a link ApplySite made is removed. A zero enable leaves
// sites-enabled alone; a link already there that points elsewhere, or a
// file in its place, is refused rather than replaced.
func ApplySite(ctx context.Context, d sysexec.Deps, changes []configtx.Change, enable Link) error {
	return configtx.WithReloadLock(ctx, d.Paths, func() error {
		made, err := makeLink(enable)
		if err != nil {
			return err
		}
		err = configtx.Apply(ctx, changes, func(ctx context.Context) error {
			// configtx tests again after restoring the files. A link made
			// here to a vhost that is gone again would fail that test.
			if made && !exists(enable.Target) {
				if err := os.Remove(enable.Path); err != nil {
					return err
				}
				made = false
			}
			return Test(ctx, d)
		})
		if err != nil {
			if made {
				if rerr := os.Remove(enable.Path); rerr != nil && !errors.Is(rerr, fs.ErrNotExist) {
					err = fmt.Errorf("%w; removing %s: %v", err, enable.Path, rerr)
				}
			}
			return err
		}
		return reload(ctx, d)
	})
}

// makeLink points enable.Path at enable.Target, and reports whether it made
// the link. One that already points there is left as it is.
func makeLink(enable Link) (bool, error) {
	if enable.Path == "" {
		return false, nil
	}
	target, err := os.Readlink(enable.Path)
	switch {
	case err == nil && target == enable.Target:
		return false, nil
	case err == nil:
		return false, fmt.Errorf("%s points at %s, not %s", enable.Path, target, enable.Target)
	case errors.Is(err, fs.ErrNotExist):
		if err := os.Symlink(enable.Target, enable.Path); err != nil {
			return false, err
		}
		return true, nil
	default:
		return false, fmt.Errorf("%s exists and isn't a symlink", enable.Path)
	}
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// DisableSite takes a site out of sites-enabled, tests Nginx and reloads
// it, under the reload lock. If the test fails, the link comes back. The
// vhost it pointed at isn't included any more, so removing it needs no
// test; that is the caller's. A link that is already gone changes nothing.
func DisableSite(ctx context.Context, d sysexec.Deps, enabled Link) error {
	return configtx.WithReloadLock(ctx, d.Paths, func() error {
		target, err := os.Readlink(enabled.Path)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			return nil
		case err != nil:
			return fmt.Errorf("%s exists and isn't a symlink", enabled.Path)
		case target != enabled.Target:
			return fmt.Errorf("%s points at %s, not %s", enabled.Path, target, enabled.Target)
		}
		if err := os.Remove(enabled.Path); err != nil {
			return err
		}
		if err := Test(ctx, d); err != nil {
			if rerr := os.Symlink(target, enabled.Path); rerr != nil {
				err = fmt.Errorf("%w; restoring %s: %v", err, enabled.Path, rerr)
			}
			return err
		}
		return reload(ctx, d)
	})
}
