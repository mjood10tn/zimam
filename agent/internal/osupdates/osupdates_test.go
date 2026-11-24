package osupdates

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"zimam/agent/contract"
	"zimam/agent/internal/paths"
	"zimam/agent/internal/sysexec"
)

var testNow = time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)

type env struct {
	d   sysexec.Deps
	run *sysexec.FakeRunner
}

func newEnv(t *testing.T, scripts ...sysexec.Script) env {
	t.Helper()
	p := paths.Paths{Root: t.TempDir()}
	for _, dir := range []string{filepath.Dir(p.AptAutoUpgrades()), p.LocksDir(), p.StateDir(), filepath.Dir(p.UnattendedUpgrade())} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	run := sysexec.NewFakeRunner(scripts...)
	return env{d: sysexec.Deps{Paths: p, Run: run, Logf: t.Logf, Now: func() time.Time { return testNow }}, run: run}
}

func touch(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, nil, 0o755); err != nil {
		t.Fatal(err)
	}
}

func dump(out string) sysexec.Script {
	return sysexec.Script{Path: aptConfig, Args: []string{"dump"}, Result: sysexec.Result{Stdout: []byte(out)}}
}

const dumpOn = `APT::Periodic::Update-Package-Lists "1";
APT::Periodic::Unattended-Upgrade "1";
Unattended-Upgrade::Automatic-Reboot "true";
Unattended-Upgrade::Automatic-Reboot-Time "03:30";
`

const dumpOff = `APT::Periodic::Update-Package-Lists "0";
APT::Periodic::Unattended-Upgrade "0";
Unattended-Upgrade::Automatic-Reboot "false";
`

func timers() sysexec.Script {
	return sysexec.Script{Path: systemctl, Args: []string{"enable", "--now", "apt-daily.timer", "apt-daily-upgrade.timer"}}
}

func called(e env, path string, args ...string) bool {
	return slices.ContainsFunc(e.run.Calls(), func(c sysexec.Call) bool { return c.Path == path && slices.Equal(c.Args, args) })
}

func TestAutoWritesDropInsAndEnablesTimers(t *testing.T) {
	e := newEnv(t, dump(dumpOn), timers())
	touch(t, e.d.Paths.UnattendedUpgrade())
	got, err := Auto(context.Background(), e.d, contract.UpdatesAutoParams{Enabled: true, AutoReboot: true, RebootTime: "03:30"})
	if err != nil {
		t.Fatal(err)
	}
	if got != (contract.AutoUpdates{Enabled: true, AutoReboot: true, RebootTime: "03:30"}) {
		t.Errorf("Auto = %+v", got)
	}
	b, _ := os.ReadFile(e.d.Paths.AptAutoUpgrades())
	r, _ := os.ReadFile(e.d.Paths.AptUnattendedReboot())
	if !strings.Contains(string(b), `Unattended-Upgrade "1";`) || !strings.Contains(string(r), `Automatic-Reboot-Time "03:30";`) {
		t.Errorf("drop-ins:\n%s\n%s", b, r)
	}
	// TestAutoEnablesTimers.
	if !called(e, systemctl, "enable", "--now", "apt-daily.timer", "apt-daily-upgrade.timer") {
		t.Error("the apt timers weren't enabled")
	}
}

func TestAutoInstallsPackageWhenMissing(t *testing.T) {
	e := newEnv(t, dump(dumpOff),
		sysexec.Script{Path: aptGet, AnyArgs: true})
	if _, err := Auto(context.Background(), e.d, contract.UpdatesAutoParams{}); err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(e.run.Calls(), func(c sysexec.Call) bool { return c.Path == aptGet && slices.Contains(c.Args, "unattended-upgrades") }) {
		t.Errorf("unattended-upgrades wasn't installed: %v", e.run.Calls())
	}
	if called(e, systemctl, "enable", "--now", "apt-daily.timer", "apt-daily-upgrade.timer") {
		t.Error("timers enabled while updates are off")
	}
}

func TestAutoBadConfigRestoresPrevious(t *testing.T) {
	// apt-config still reads the old values: something overrides Zimam's file.
	e := newEnv(t, dump(dumpOff), dump(dumpOff))
	touch(t, e.d.Paths.UnattendedUpgrade())
	if err := os.WriteFile(e.d.Paths.AptAutoUpgrades(), []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Auto(context.Background(), e.d, contract.UpdatesAutoParams{Enabled: true}); err == nil {
		t.Fatal("Auto succeeded though apt-config didn't read the new values")
	}
	if b, _ := os.ReadFile(e.d.Paths.AptAutoUpgrades()); string(b) != "old\n" {
		t.Errorf("drop-in is %q, want the old one back", b)
	}
	if _, err := os.Stat(e.d.Paths.AptUnattendedReboot()); err == nil {
		t.Error("the new reboot drop-in stayed")
	}
}

func TestReadAuto(t *testing.T) {
	e := newEnv(t, dump(dumpOn))
	got, err := ReadAuto(context.Background(), e.d)
	if err != nil || got != (contract.AutoUpdates{Enabled: true, AutoReboot: true, RebootTime: "03:30"}) {
		t.Errorf("ReadAuto = %+v, %v", got, err)
	}
	// A reboot time without the reboot is not reported.
	e = newEnv(t, dump("APT::Periodic::Unattended-Upgrade \"1\";\nAPT::Periodic::Update-Package-Lists \"1\";\nUnattended-Upgrade::Automatic-Reboot-Time \"02:00\";\n"))
	got, _ = ReadAuto(context.Background(), e.d)
	if got != (contract.AutoUpdates{Enabled: true}) {
		t.Errorf("ReadAuto = %+v", got)
	}
}

func dpkgQuery(out string) sysexec.Script {
	return sysexec.Script{Path: dpkgQueryBin, Args: dpkgQueryArgs, Result: sysexec.Result{Stdout: []byte(out)}}
}

func installScripts(scope contract.UpdatesScope, upgradeOut string) []sysexec.Script {
	upgrade := sysexec.Script{Path: unattendedUpgrade, Args: []string{"--verbose"}, Result: sysexec.Result{Stdout: []byte(upgradeOut)}}
	if scope == "all" {
		upgrade = sysexec.Script{Path: aptGet, Args: allUpgradeArgs(), Result: sysexec.Result{Stdout: []byte(upgradeOut)}}
	}
	return []sysexec.Script{
		dpkgQuery("gzip\t1.14-1\tii \nopenssl\t3.5.0-1\tii \nold-gone\t1.0\trc \n"),
		{Path: aptGet, Args: []string{"-o", "DPkg::Lock::Timeout=300", "update"}},
		upgrade,
		dpkgQuery("gzip\t1.14-1.1\tii \nopenssl\t3.5.0-1\tii \nlinux-image-7.0.0-9\t7.0.0-9.9\tii \nold-gone\t1.0\trc \n"),
		{Path: aptCheckBin, Result: sysexec.Result{Stderr: []byte("3;0")}},
	}
}

const restartOut = "Unpacking gzip ...\nRestarting services...\n systemctl restart nginx.service php8.4-fpm.service\nNo containers need to be restarted.\n"

func TestInstallSecurityRunsUnattendedUpgrade(t *testing.T) {
	e := newEnv(t, installScripts("security", restartOut)...)
	touch(t, e.d.Paths.AptCheck())
	touch(t, e.d.Paths.RebootRequired())
	res, err := Install(context.Background(), e.d, "security")
	if err != nil {
		t.Fatal(err)
	}
	// TestInstallReportsUpgradedFromDpkgDiff.
	want := []contract.UpgradedPackage{{Package: "gzip", From: "1.14-1", To: "1.14-1.1"}, {Package: "linux-image-7.0.0-9", From: "", To: "7.0.0-9.9"}}
	if !slices.Equal(res.Upgraded, want) {
		t.Errorf("upgraded = %+v", res.Upgraded)
	}
	if !slices.Equal(res.Restarted, []string{"nginx.service", "php8.4-fpm.service"}) {
		t.Errorf("restarted = %v", res.Restarted)
	}
	// TestInstallReportsRebootRequired, TestInstallRefreshesCounts.
	if !res.RebootRequired || res.SecurityPendingAfter == nil || *res.SecurityPendingAfter != 0 {
		t.Errorf("result = %+v", res)
	}
	b, err := os.ReadFile(e.d.Paths.NeedrestartConf())
	if err != nil || !strings.Contains(string(b), "qr(^zimam-)") {
		t.Errorf("needrestart conf %q, %v", b, err)
	}
	// TestInstallEnvironmentNoninteractive.
	for _, c := range e.run.Calls() {
		if (c.Path == aptGet || c.Path == unattendedUpgrade) && !slices.Contains(c.Env, "DEBIAN_FRONTEND=noninteractive") {
			t.Errorf("%s %v ran without DEBIAN_FRONTEND=noninteractive", c.Path, c.Args)
		}
	}
}

func TestInstallAllUsesConfoldAndNeverDistUpgrade(t *testing.T) {
	e := newEnv(t, installScripts("all", "")...)
	touch(t, e.d.Paths.AptCheck())
	res, err := Install(context.Background(), e.d, "all")
	if err != nil {
		t.Fatal(err)
	}
	if res.RebootRequired || len(res.Restarted) != 0 {
		t.Errorf("result = %+v", res)
	}
	for _, c := range e.run.Calls() {
		if c.Path == unattendedUpgrade || slices.Contains(c.Args, "dist-upgrade") || slices.Contains(c.Args, "full-upgrade") {
			t.Errorf("ran %s %v", c.Path, c.Args)
		}
	}
	if !called(e, aptGet, allUpgradeArgs()...) {
		t.Error("apt-get upgrade with --force-confold didn't run")
	}
}

func TestRebootAtRendersShutdownArgv(t *testing.T) {
	e := newEnv(t, sysexec.Script{Path: shutdown, Args: []string{"-r", "03:30"}})
	res, err := Reboot(context.Background(), e.d, "03:30")
	if err != nil {
		t.Fatal(err)
	}
	// The next 03:30 in the server's zone, within 24 hours.
	at := res.ScheduledAt.In(time.Local)
	if at.Hour() != 3 || at.Minute() != 30 || !at.After(testNow) || at.Sub(testNow) > 24*time.Hour {
		t.Errorf("scheduled at %v", res.ScheduledAt)
	}
}

func TestRebootNowAfterResultWritten(t *testing.T) {
	e := newEnv(t, sysexec.Script{Path: systemdRun, AnyArgs: true})
	res, err := Reboot(context.Background(), e.d, "now")
	if err != nil {
		t.Fatal(err)
	}
	// The reboot is a timer a few seconds on, so the job's result and audit
	// entry are written first.
	c := e.run.Calls()[0]
	if !slices.Contains(c.Args, "--on-active=15s") {
		t.Errorf("systemd-run %v", c.Args)
	}
	if !slices.Equal(c.Args[len(c.Args)-2:], []string{systemctl, "reboot"}) || !res.ScheduledAt.Equal(testNow.Add(rebootDelay)) {
		t.Errorf("reboot %v at %v", c.Args, res.ScheduledAt)
	}
}

func TestRebootCancel(t *testing.T) {
	e := newEnv(t, sysexec.Script{Path: shutdown, Args: []string{"-c"}}, sysexec.Script{Path: systemctl, Args: []string{"stop", "zimam-reboot.timer"}})
	writeScheduled(t, e.d.Paths)
	res, err := Cancel(context.Background(), e.d)
	if err != nil || !res.Cancelled {
		t.Errorf("Cancel = %+v, %v", res, err)
	}
	if !called(e, shutdown, "-c") {
		t.Error("shutdown -c didn't run")
	}
}

func writeScheduled(t *testing.T, p paths.Paths) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p.ShutdownScheduled()), 0o755); err != nil {
		t.Fatal(err)
	}
	usec := time.Date(2026, 11, 25, 3, 30, 0, 0, time.UTC).UnixMicro()
	if err := os.WriteFile(p.ShutdownScheduled(), []byte("USEC="+strconv.FormatInt(usec, 10)+"\nWARN_WALL=1\nMODE=reboot\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestStatusReportsScheduledReboot(t *testing.T) {
	e := newEnv(t)
	if at := ScheduledReboot(e.d.Paths); at != nil {
		t.Errorf("no file: %v", at)
	}
	writeScheduled(t, e.d.Paths)
	if at := ScheduledReboot(e.d.Paths); at == nil || !at.Equal(time.Date(2026, 11, 25, 3, 30, 0, 0, time.UTC)) {
		t.Errorf("ScheduledReboot = %v", at)
	}
	if err := os.WriteFile(e.d.Paths.ShutdownScheduled(), []byte("USEC=1\nMODE=poweroff\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if at := ScheduledReboot(e.d.Paths); at != nil {
		t.Errorf("a poweroff counted as a reboot: %v", at)
	}
}
