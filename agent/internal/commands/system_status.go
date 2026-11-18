package commands

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"regexp"
	"slices"
	"strings"

	"zimam/agent/contract"
	"zimam/agent/internal/config"
	"zimam/agent/internal/maintenance"
	"zimam/agent/internal/node"
	"zimam/agent/internal/php"
	"zimam/agent/internal/release"
	"zimam/agent/internal/services"
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
		svc := servicesStatus(ctx, d)
		nodeSt := nodeStatus(ctx, d)
		reboot, err := exists(d.Paths.RebootRequired())
		if err != nil {
			return nil, err
		}
		return contract.SystemStatusResult{
			AgentVersion:   release.Version,
			Protocols:      slices.Clone(contract.SupportedProtocols),
			NginxVersion:   nginxVersion(ctx, d),
			PHP:            php,
			Services:       svc,
			Node:           nodeSt,
			Timezone:       serverTimezone(d),
			RebootRequired: reboot,
			Updates:        updateCounts(d),
			PanelDomain:    cfg.PanelDomain,
			ACMEDirectory:  cfg.ACMEDirectory,
		}, nil
	}
}

// zoneName is an IANA zone as a path under zoneinfo: segments of letters,
// digits and _+-, so a link pointing anywhere else is not taken as one.
var zoneName = regexp.MustCompile(`^[A-Za-z0-9_+-]+(/[A-Za-z0-9_+-]+)*$`)

// serverTimezone is the zone cron runs jobs in (ruling R7): the part of
// /etc/localtime's link target after /zoneinfo/, absolute as Debian writes it
// and relative as timedatectl does. UTC when it is not a link or names no zone.
func serverTimezone(d sysexec.Deps) string {
	target, err := os.Readlink(d.Paths.Localtime())
	if err != nil {
		return "UTC"
	}
	_, zone, ok := strings.Cut(target, "/zoneinfo/")
	// posix/ and right/ are zoneinfo's variants of the same zone name.
	zone = strings.TrimPrefix(strings.TrimPrefix(zone, "posix/"), "right/")
	if !ok || !zoneName.MatchString(zone) {
		return "UTC"
	}
	return zone
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

// servicesStatus reports MySQL and Valkey from services.json, with each
// installed one's unit state read live. A service that was never installed
// costs no systemctl call. What can't be read is left unknown, with a log
// line, and never fails the status: a corrupt services.json leaves both
// unknown, a unit systemctl can't answer for leaves that service unknown.
func servicesStatus(ctx context.Context, d sysexec.Deps) contract.ServicesStatus {
	st, err := services.Load(d.Paths)
	if err != nil {
		d.Logf("system.status: services unknown: %v", err)
		return contract.ServicesStatus{}
	}
	one := func(name string, e services.Entry, unit string) *contract.ServiceStatus {
		if !e.Installed {
			return &contract.ServiceStatus{}
		}
		active, err := unitActive(ctx, d, unit)
		if err != nil {
			d.Logf("system.status: %s state unknown: %v", name, err)
			return nil
		}
		return &contract.ServiceStatus{Installed: true, Version: e.Version, Active: active}
	}
	return contract.ServicesStatus{
		MySQL:  one("mysql", st.MySQL, "mysql"),
		Valkey: one("valkey", st.Valkey, "valkey-server"),
	}
}

// nodeStatus reports Node.js live (R04-D6, ruling R1): the installed nodejs
// package's version and major, and the allowlist with each major's end of
// life. dpkg-query that fails leaves it unknown (nil) with a log line, as
// nginx's version is, and never fails the status.
func nodeStatus(ctx context.Context, d sysexec.Deps) *contract.NodeStatus {
	version, err := node.Installed(ctx, d)
	if err != nil {
		d.Logf("system.status: Node.js unknown: %v", err)
		return nil
	}
	st := &contract.NodeStatus{Installed: version != "", Available: []contract.AvailableNode{}}
	if st.Installed {
		st.Version, st.Major = version, string(node.MajorOf(version))
	}
	now := d.Now()
	for _, r := range contract.NodeAllowlist {
		st.Available = append(st.Available, contract.AvailableNode{Major: r.Major, EndOfLife: r.EndOfLife, Supported: r.SupportedOn(now), Recommended: r.Major == contract.DefaultNodeMajor})
	}
	return st
}
