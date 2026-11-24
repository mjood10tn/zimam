// Package osupdates keeps the server's packages up to date (R06-D12..D16):
// Ubuntu's own automatic security updates, turned on or off through apt
// drop-ins; installing pending updates now; and reboots, now or at a time.
package osupdates

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"zimam/agent/contract"
	"zimam/agent/internal/configtx"
	"zimam/agent/internal/maintenance"
	"zimam/agent/internal/php"
	"zimam/agent/internal/sysexec"
	"zimam/agent/internal/systemd"
	"zimam/agent/internal/templates"
)

const (
	aptGet            = php.AptGet
	aptConfig         = "/usr/bin/apt-config"
	aptCheckBin       = "/usr/lib/update-notifier/apt-check"
	dpkgQueryBin      = "/usr/bin/dpkg-query"
	unattendedUpgrade = "/usr/bin/unattended-upgrade"
	shutdown          = "/usr/sbin/shutdown"
	systemctl         = "/usr/bin/systemctl"
	systemdRun        = "/usr/bin/systemd-run"
)

// Auto writes 21zimam-auto-upgrades and 52zimam-unattended-reboot,
// installing unattended-upgrades first if it is missing. The test is
// apt-config dump reading the new values back: a file sorting after
// Zimam's that overrides them fails it, and the old files come back. With
// updates on, apt's two timers are enabled.
func Auto(ctx context.Context, d sysexec.Deps, p contract.UpdatesAutoParams) (contract.AutoUpdates, error) {
	want := contract.AutoUpdates{Enabled: p.Enabled, AutoReboot: p.AutoReboot}
	if p.AutoReboot {
		want.RebootTime = p.RebootTime
	}
	if _, err := os.Stat(d.Paths.UnattendedUpgrade()); errors.Is(err, fs.ErrNotExist) {
		if err := php.InstallPackages(ctx, d, "unattended-upgrades"); err != nil {
			return contract.AutoUpdates{}, err
		}
	}
	periodic, err := templates.Render("apt/21zimam-auto-upgrades", templates.AutoUpgrades{Enabled: p.Enabled})
	if err != nil {
		return contract.AutoUpdates{}, err
	}
	reboot, err := templates.Render("apt/52zimam-unattended-reboot", templates.UnattendedReboot{AutoReboot: want.AutoReboot, RebootTime: want.RebootTime})
	if err != nil {
		return contract.AutoUpdates{}, err
	}
	if err := os.MkdirAll(filepath.Dir(d.Paths.AptAutoUpgrades()), 0o755); err != nil {
		return contract.AutoUpdates{}, err
	}
	first := true
	err = configtx.Apply(ctx, []configtx.Change{
		configtx.RootFile(d.Paths.AptAutoUpgrades(), periodic, 0o644),
		configtx.RootFile(d.Paths.AptUnattendedReboot(), reboot, 0o644),
	}, func(ctx context.Context) error {
		// After a failure the old files only need to be back.
		if !first {
			return nil
		}
		first = false
		got, err := ReadAuto(ctx, d)
		if err != nil {
			return &configtx.TestFailed{Output: err.Error()}
		}
		if got != want {
			return &configtx.TestFailed{Output: fmt.Sprintf("apt-config reads %+v, not %+v: another apt.conf.d file overrides Zimam's", got, want)}
		}
		return nil
	})
	if err != nil {
		return contract.AutoUpdates{}, err
	}
	if p.Enabled {
		if err := systemd.Systemctl(ctx, d, "enable", "--now", "apt-daily.timer", "apt-daily-upgrade.timer"); err != nil {
			return contract.AutoUpdates{}, err
		}
	}
	return want, nil
}

// ReadAuto reads apt's effective settings from apt-config dump: updates are
// on when both periodic values are set and not "0".
func ReadAuto(ctx context.Context, d sysexec.Deps) (contract.AutoUpdates, error) {
	res, err := d.Run.Run(ctx, sysexec.Cmd{Path: aptConfig, Args: []string{"dump"}})
	if err != nil {
		return contract.AutoUpdates{}, fmt.Errorf("apt-config dump: %w", err)
	}
	conf := map[string]string{}
	for line := range strings.Lines(string(res.Stdout)) {
		k, v, ok := strings.Cut(strings.TrimSpace(line), " ")
		if ok {
			conf[k] = strings.TrimSuffix(strings.Trim(strings.TrimSuffix(v, ";"), `"`), `"`)
		}
	}
	on := func(k string) bool { v := conf[k]; return v != "" && v != "0" }
	a := contract.AutoUpdates{
		Enabled:    on("APT::Periodic::Update-Package-Lists") && on("APT::Periodic::Unattended-Upgrade"),
		AutoReboot: conf["Unattended-Upgrade::Automatic-Reboot"] == "true",
	}
	if t := contract.ClockTime(conf["Unattended-Upgrade::Automatic-Reboot-Time"]); a.AutoReboot && t.Validate() == nil {
		a.RebootTime = t
	}
	return a, nil
}

var (
	// dpkgQueryArgs lists every package with its version and status.
	dpkgQueryArgs = []string{"-W", "-f=${Package}\t${Version}\t${db:Status-Abbrev}\n"}
	aptUpdateArgs = append(slices.Clone(php.AptLockWait), "update")
)

// allUpgradeArgs is apt-get upgrade, never dist-upgrade: nothing is removed.
// A changed config file keeps the owner's version, so nothing stops to ask.
func allUpgradeArgs() []string {
	return append(slices.Clone(php.AptLockWait), "-o", "Dpkg::Options::=--force-confdef", "-o", "Dpkg::Options::=--force-confold", "-y", "upgrade")
}

// Install installs the pending updates (R06-D13): Ubuntu's security
// origins through unattended-upgrade, or every upgrade apt-get upgrade
// offers. needrestart restarts the services that need it but never Zimam's
// units. It reports what changed and refreshes the pending counts at once.
func Install(ctx context.Context, d sysexec.Deps, scope contract.UpdatesScope) (contract.UpdatesInstallResult, error) {
	res := contract.UpdatesInstallResult{Scope: scope, Upgraded: []contract.UpgradedPackage{}, Restarted: []string{}}
	if err := writeNeedrestart(ctx, d); err != nil {
		return res, err
	}
	before, err := installed(ctx, d)
	if err != nil {
		return res, err
	}
	d.Logf("Refreshing the package lists")
	if _, err := d.Run.Run(ctx, sysexec.Cmd{Path: aptGet, Args: aptUpdateArgs, Env: php.AptEnv}); err != nil {
		return res, err
	}
	cmd := sysexec.Cmd{Path: aptGet, Args: allUpgradeArgs(), Env: php.AptEnv}
	if scope == "security" {
		cmd = sysexec.Cmd{Path: unattendedUpgrade, Args: []string{"--verbose"}, Env: php.AptEnv}
	}
	d.Logf("Running %s %s", cmd.Path, strings.Join(cmd.Args, " "))
	out, err := d.Run.Run(ctx, cmd)
	for line := range strings.Lines(string(out.Stdout)) {
		d.Logf("%s", strings.TrimRight(line, "\n"))
	}
	if err != nil {
		return res, err
	}
	after, err := installed(ctx, d)
	if err != nil {
		return res, err
	}
	res.Upgraded = diff(before, after)
	res.Restarted = restarted(out.Stdout)
	_, err = os.Stat(d.Paths.RebootRequired())
	res.RebootRequired = err == nil
	if counts, err := maintenance.CountUpdatesNow(ctx, d); err != nil {
		d.Logf("Counting the pending updates again failed: %v", err)
	} else {
		res.SecurityPendingAfter = counts.Security
	}
	d.Logf("%d packages upgraded, %d services restarted", len(res.Upgraded), len(res.Restarted))
	return res, nil
}

// writeNeedrestart puts needrestart's Zimam drop-in in place (R06-D13); it
// has no config test of its own.
func writeNeedrestart(ctx context.Context, d sysexec.Deps) error {
	conf, err := templates.Render("needrestart/50-zimam.conf", nil)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(d.Paths.NeedrestartConf()), 0o755); err != nil {
		return err
	}
	return configtx.Apply(ctx, []configtx.Change{configtx.RootFile(d.Paths.NeedrestartConf(), conf, 0o644)},
		func(context.Context) error { return nil })
}

type pkg struct{ name, version string }

// installed lists the installed packages ("ii") in dpkg-query's order.
func installed(ctx context.Context, d sysexec.Deps) ([]pkg, error) {
	res, err := d.Run.Run(ctx, sysexec.Cmd{Path: dpkgQueryBin, Args: dpkgQueryArgs})
	if err != nil {
		return nil, fmt.Errorf("listing packages: %w", err)
	}
	var out []pkg
	s := bufio.NewScanner(bytes.NewReader(res.Stdout))
	for s.Scan() {
		f := strings.Split(s.Text(), "\t")
		if len(f) == 3 && strings.HasPrefix(f[2], "ii") {
			out = append(out, pkg{f[0], f[1]})
		}
	}
	return out, s.Err()
}

// diff lists the packages whose version changed, and the new ones, sorted.
func diff(before, after []pkg) []contract.UpgradedPackage {
	was := map[string]string{}
	for _, p := range before {
		was[p.name] = p.version
	}
	out := []contract.UpgradedPackage{}
	for _, p := range after {
		if v, ok := was[p.name]; !ok || v != p.version {
			out = append(out, contract.UpgradedPackage{Package: p.name, From: v, To: p.version})
		}
	}
	slices.SortFunc(out, func(a, b contract.UpgradedPackage) int { return strings.Compare(a.Package, b.Package) })
	return out
}

// restarted reads needrestart's " systemctl restart <unit>..." lines after
// "Restarting services..." (spike), but not those under "Service restarts
// being deferred:", which it didn't run.
func restarted(stdout []byte) []string {
	out := []string{}
	in := false
	for line := range strings.Lines(string(stdout)) {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "Restarting services..."):
			in = true
		case strings.HasPrefix(line, "Service restarts being deferred"):
			in = false
		case in && strings.HasPrefix(line, "systemctl restart "):
			for _, u := range strings.Fields(strings.TrimPrefix(line, "systemctl restart ")) {
				if !slices.Contains(out, u) {
					out = append(out, u)
				}
			}
		}
	}
	return out
}

// rebootDelay lets a reboot "now" write the job's result and audit entry
// first (R06-D14).
const rebootDelay = 15 * time.Second

const rebootUnit = "zimam-reboot"

// Reboot reboots now, as a timer rebootDelay on, or at the next HH:MM in
// the server's zone through shutdown -r.
func Reboot(ctx context.Context, d sysexec.Deps, at contract.RebootAt) (contract.SystemRebootResult, error) {
	now := d.Now()
	if at == "now" {
		_, err := d.Run.Run(ctx, sysexec.Cmd{Path: systemdRun, Args: []string{
			"--unit=" + rebootUnit, "--on-active=" + strconv.Itoa(int(rebootDelay.Seconds())) + "s",
			"--timer-property=AccuracySec=1s", "--quiet", "--", systemctl, "reboot",
		}})
		if err != nil {
			return contract.SystemRebootResult{}, err
		}
		d.Logf("The server reboots in %s", rebootDelay)
		return contract.SystemRebootResult{At: at, ScheduledAt: now.Add(rebootDelay).UTC()}, nil
	}
	if _, err := d.Run.Run(ctx, sysexec.Cmd{Path: shutdown, Args: []string{"-r", string(at)}}); err != nil {
		return contract.SystemRebootResult{}, err
	}
	h, _ := strconv.Atoi(string(at[:2]))
	m, _ := strconv.Atoi(string(at[3:]))
	local := now.In(time.Local)
	next := time.Date(local.Year(), local.Month(), local.Day(), h, m, 0, 0, time.Local)
	if !next.After(local) {
		next = next.AddDate(0, 0, 1)
	}
	d.Logf("The server reboots at %s", next.Format(time.DateTime))
	return contract.SystemRebootResult{At: at, ScheduledAt: next.UTC()}, nil
}

// Cancel cancels a scheduled reboot, unattended-upgrades' automatic one
// included (spike), and a reboot "now" still waiting on its timer.
func Cancel(ctx context.Context, d sysexec.Deps) (contract.SystemRebootCancelResult, error) {
	scheduled := ScheduledReboot(d.Paths) != nil
	// Stopping a timer that isn't loaded fails: then no reboot "now" waits.
	timer := systemd.Systemctl(ctx, d, "stop", rebootUnit+".timer") == nil
	if _, err := d.Run.Run(ctx, sysexec.Cmd{Path: shutdown, Args: []string{"-c"}}); err != nil {
		return contract.SystemRebootCancelResult{}, err
	}
	return contract.SystemRebootCancelResult{Cancelled: scheduled || timer}, nil
}

// ScheduledReboot reads /run/systemd/shutdown/scheduled: USEC= and
// MODE=reboot. Nil when none is scheduled or it can't be read.
func ScheduledReboot(p interface{ ShutdownScheduled() string }) *time.Time {
	b, err := os.ReadFile(p.ShutdownScheduled())
	if err != nil {
		return nil
	}
	var usec int64
	var mode string
	for line := range strings.Lines(string(b)) {
		k, v, _ := strings.Cut(strings.TrimSpace(line), "=")
		switch k {
		case "USEC":
			usec, _ = strconv.ParseInt(v, 10, 64)
		case "MODE":
			mode = v
		}
	}
	if mode != "reboot" || usec <= 0 {
		return nil
	}
	t := time.UnixMicro(usec).UTC()
	return &t
}
