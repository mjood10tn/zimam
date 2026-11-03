package commands

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"slices"
	"strings"

	"zimam/agent/contract"
	"zimam/agent/internal/config"
	"zimam/agent/internal/maintenance"
	"zimam/agent/internal/php"
	"zimam/agent/internal/release"
	"zimam/agent/internal/sysexec"
)

const (
	nginx     = "/usr/sbin/nginx"
	systemctl = "/usr/bin/systemctl"
)

// systemStatus answers system.status (§7.5). It reads agent.json on every
// call, since php.default and logs.policy jobs change it while serve runs.
func systemStatus(d sysexec.Deps) Handler {
	return func(ctx context.Context, _ contract.Params) (any, error) {
		cfg, err := config.Load(d.Paths)
		if err != nil {
			return nil, err
		}
		php, err := phpStatus(ctx, d, cfg)
		if err != nil {
			return nil, err
		}
		reboot, err := exists(d.Paths.RebootRequired())
		if err != nil {
			return nil, err
		}
		return contract.SystemStatusResult{
			AgentVersion:   release.Version,
			Protocols:      slices.Clone(contract.SupportedProtocols),
			NginxVersion:   nginxVersion(ctx, d),
			PHP:            php,
			RebootRequired: reboot,
			Updates:        updateCounts(d),
			PanelDomain:    cfg.PanelDomain,
			ACMEDirectory:  cfg.ACMEDirectory,
		}, nil
	}
}

// phpStatus lists the installed versions, each with whether its FPM service
// runs, and the allowlist with each version's end of support, so the panel
// never hard-codes it (D19). A version is installed while its php-fpm
// binary exists, and in use while it has a Zimam pool.
func phpStatus(ctx context.Context, d sysexec.Deps, cfg config.Agent) (contract.PHPStatus, error) {
	st := contract.PHPStatus{
		Installed: []contract.InstalledPHP{},
		Default:   cfg.DefaultPHP,
		Panel:     cfg.PanelPHP,
		Available: []contract.AvailablePHP{},
	}
	now := d.Now()
	for _, r := range contract.PHPAllowlist {
		st.Available = append(st.Available, contract.AvailablePHP{Version: r.Version, SecurityEnd: r.SecurityEnd, Supported: r.SupportedOn(now)})
		installed, err := php.Installed(d, r.Version)
		if err != nil {
			return contract.PHPStatus{}, err
		}
		if !installed {
			continue
		}
		active, err := unitActive(ctx, d, "php"+string(r.Version)+"-fpm")
		if err != nil {
			return contract.PHPStatus{}, err
		}
		inUse, err := php.HasPools(d, r.Version)
		if err != nil {
			return contract.PHPStatus{}, err
		}
		st.Installed = append(st.Installed, contract.InstalledPHP{Version: r.Version, FPMActive: active, InUse: inUse})
	}
	return st, nil
}

// unitActive runs systemctl is-active: exit 0 means the unit is active, any
// other exit code that it isn't.
func unitActive(ctx context.Context, d sysexec.Deps, unit string) (bool, error) {
	_, err := d.Run.Run(ctx, sysexec.Cmd{Path: systemctl, Args: []string{"is-active", "--quiet", unit}})
	var exit *sysexec.ExitError
	switch {
	case err == nil:
		return true, nil
	case errors.As(err, &exit):
		return false, nil
	default:
		return false, err
	}
}

// nginxVersion is the version nginx -v prints on stderr, "nginx version:
// nginx/1.28.3 (Ubuntu)" on Ubuntu 26.04. It is "" when that fails: one
// tool that fails shouldn't blank the dashboard.
func nginxVersion(ctx context.Context, d sysexec.Deps) string {
	res, err := d.Run.Run(ctx, sysexec.Cmd{Path: nginx, Args: []string{"-v"}})
	if err != nil {
		d.Logf("system.status: %v", err)
		return ""
	}
	rest, ok := strings.CutPrefix(string(res.Stderr), "nginx version: nginx/")
	version, _, _ := strings.Cut(strings.TrimSpace(rest), " ")
	if !ok || version == "" {
		d.Logf("system.status: nginx -v printed %q", res.Stderr)
		return ""
	}
	return version
}

// updateCounts are the counts maintenance last stored. An unreadable
// updates.json reads as never checked; maintenance writes it again within
// 15 minutes.
func updateCounts(d sysexec.Deps) contract.UpdateCounts {
	u, err := maintenance.ReadUpdates(d.Paths)
	if err != nil {
		d.Logf("system.status: %v", err)
		return contract.UpdateCounts{}
	}
	return u
}

func exists(path string) (bool, error) {
	_, err := os.Stat(path)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, fs.ErrNotExist):
		return false, nil
	default:
		return false, err
	}
}
