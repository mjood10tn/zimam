package php

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"zimam/agent/contract"
	"zimam/agent/internal/configtx"
	"zimam/agent/internal/sysexec"
)

// installEnv is newEnv after `apt-get install php8.4-fpm`: the stock
// www.conf exists, plus a Zimam pool when pools is set. Every command
// Install may run is scripted; active is what is-active answers.
func installEnv(t *testing.T, pools, active bool) *testEnv {
	t.Helper()
	e := newEnv(t, fixture(t, "sury-apt.gpg"), "/php/")
	writeFile(t, e.p.PHPWwwPool("8.4"), "[www]\n")
	if pools {
		writeFile(t, filepath.Join(e.p.PHPPoolDir("8.4"), "zimam-shop.conf"), "[zimam-shop]\n")
	}
	isActive := systemctlCmd("is-active", "--quiet", "php8.4-fpm")
	if !active {
		isActive.Result.ExitCode = 3
	}
	for _, s := range []sysexec.Script{
		aptUpdate, aptInstall("8.4"), fpmTest("8.4"), isActive,
		systemctlCmd("reload", "php8.4-fpm"), systemctlCmd("enable", "--now", "php8.4-fpm"), systemctlCmd("disable", "--now", "php8.4-fpm"),
	} {
		e.run.Add(s)
	}
	return e
}

func TestInstallCommandSequence(t *testing.T) {
	e := installEnv(t, true, true)
	if err := Install(context.Background(), e.d, "8.4"); err != nil {
		t.Fatalf("Install: %v", err)
	}
	want := []string{
		"/usr/bin/apt-get -o DPkg::Lock::Timeout=300 update",
		"/usr/bin/apt-get -o DPkg::Lock::Timeout=300 -o Dpkg::Options::=--force-confdef -o Dpkg::Options::=--force-confold install -y --no-install-recommends " + strings.Join(Packages("8.4"), " "),
		"/usr/sbin/php-fpm8.4 -t", // the configtx test of the ini and global files
		"/usr/sbin/php-fpm8.4 -t", // ApplyFPM's test before the reload
		"/usr/bin/systemctl is-active --quiet php8.4-fpm",
		"/usr/bin/systemctl reload php8.4-fpm",
	}
	calls := e.run.Calls()
	if got := commands(calls); !slices.Equal(got, want) {
		t.Fatalf("ran\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	for _, c := range calls[:2] {
		if !slices.Contains(c.Env, "DEBIAN_FRONTEND=noninteractive") {
			t.Errorf("%s ran with env %q", c.Path, c.Env)
		}
	}
}

func TestInstallRemovesWwwConf(t *testing.T) {
	e := installEnv(t, false, false)
	if err := Install(context.Background(), e.d, "8.4"); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if _, err := os.Lstat(e.p.PHPWwwPool("8.4")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("www.conf is still there (Lstat: %v)", err)
	}
	for path, want := range map[string][]string{
		e.p.PHPIni("8.4"):        {"expose_php = Off\n", "opcache.validate_permission = 1\n", "opcache.restrict_api = /nonexistent/\n"},
		e.p.PHPGlobalConf("8.4"): {"[global]\n", "process_control_timeout = 10s\n"},
	} {
		got := readFile(t, path)
		for _, w := range want {
			if !strings.Contains(got, w) {
				t.Errorf("%s lacks %q:\n%s", path, w, got)
			}
		}
		if fi, err := os.Stat(path); err != nil {
			t.Error(err)
		} else if fi.Mode().Perm() != 0o644 {
			t.Errorf("%s: mode %v, want 0644", path, fi.Mode().Perm())
		}
	}
}

func TestInstallWithoutPoolsDisablesFPM(t *testing.T) {
	e := installEnv(t, false, true)
	if err := Install(context.Background(), e.d, "8.4"); err != nil {
		t.Fatalf("Install: %v", err)
	}
	got := commands(e.run.Calls())
	if want := line(systemctlCmd("disable", "--now", "php8.4-fpm")); lastOf(got) != want {
		t.Errorf("ran %q, want %q last", got, want)
	}
	// With no pool left, php-fpm -t exits 78 ("No pool defined"), so it must
	// not run.
	if slices.Contains(got, line(fpmTest("8.4"))) {
		t.Errorf("ran php-fpm8.4 -t without a pool: %q", got)
	}
}

func TestInstallWithPoolsReloads(t *testing.T) {
	e := installEnv(t, true, true)
	if err := Install(context.Background(), e.d, "8.4"); err != nil {
		t.Fatalf("Install: %v", err)
	}
	got := commands(e.run.Calls())
	if want := line(systemctlCmd("reload", "php8.4-fpm")); lastOf(got) != want {
		t.Errorf("ran %q, want %q last", got, want)
	}
	for _, s := range []sysexec.Script{systemctlCmd("enable", "--now", "php8.4-fpm"), systemctlCmd("disable", "--now", "php8.4-fpm")} {
		if slices.Contains(got, line(s)) {
			t.Errorf("ran %q", line(s))
		}
	}
}

func TestInstallStartsWhenInactive(t *testing.T) {
	e := installEnv(t, true, false)
	if err := Install(context.Background(), e.d, "8.4"); err != nil {
		t.Fatalf("Install: %v", err)
	}
	got := commands(e.run.Calls())
	if want := line(systemctlCmd("enable", "--now", "php8.4-fpm")); lastOf(got) != want {
		t.Errorf("ran %q, want %q last", got, want)
	}
	if slices.Contains(got, line(systemctlCmd("reload", "php8.4-fpm"))) {
		t.Errorf("reloaded an inactive service: %q", got)
	}
}

func TestInstallIsRerunnable(t *testing.T) {
	e := installEnv(t, false, false)
	if err := Install(context.Background(), e.d, "8.4"); err != nil {
		t.Fatalf("first Install: %v", err)
	}
	first := commands(e.run.Calls())
	files := map[string]string{}
	for _, path := range []string{e.p.PHPIni("8.4"), e.p.PHPGlobalConf("8.4"), e.p.SurySources(), e.p.SuryPin(), e.p.SuryKeyring()} {
		files[path] = readFile(t, path)
	}
	if err := Install(context.Background(), e.d, "8.4"); err != nil {
		t.Fatalf("second Install: %v", err)
	}
	if again := commands(e.run.Calls())[len(first):]; !slices.Equal(again, first) {
		t.Errorf("the second run ran %q, want %q again", again, first)
	}
	for path, want := range files {
		if got := readFile(t, path); got != want {
			t.Errorf("%s changed on the second run", path)
		}
	}
}

func TestInstallRefusesUnknownVersion(t *testing.T) {
	e := installEnv(t, false, false)
	for _, v := range []contract.PHPVersion{"8.1", "../8.4", "8.4; rm -rf /", ""} {
		if err := Install(context.Background(), e.d, v); err == nil {
			t.Errorf("Install(%q) succeeded", v)
		}
		if err := ApplyFPM(context.Background(), e.d, v); err == nil {
			t.Errorf("ApplyFPM(%q) succeeded", v)
		}
	}
	if calls := e.run.Calls(); len(calls) != 0 {
		t.Errorf("ran %q", commands(calls))
	}
}

func TestHasPools(t *testing.T) {
	e := installEnv(t, false, false)
	if has, err := HasPools(e.d, "8.5"); err != nil || has {
		t.Errorf("without a pool.d: HasPools = %v, %v", has, err)
	}
	writeFile(t, e.p.PHPGlobalConf("8.4"), "[global]\n")
	if has, err := HasPools(e.d, "8.4"); err != nil || has {
		t.Errorf("with www.conf and the global config only: HasPools = %v, %v", has, err)
	}
	writeFile(t, filepath.Join(e.p.PHPPoolDir("8.4"), "zimam-shop.conf"), "[zimam-shop]\n")
	if has, err := HasPools(e.d, "8.4"); err != nil || !has {
		t.Errorf("with a site pool: HasPools = %v, %v", has, err)
	}
}

func TestApplyFPMFailedTestDoesNotReload(t *testing.T) {
	e := installEnv(t, true, true)
	bad := fpmTest("8.4")
	bad.Result = sysexec.Result{ExitCode: 78, Stderr: []byte("ERROR: [pool zimam-shop] unable to parse value for entry 'pm'\n")}
	e.run = sysexec.NewFakeRunner(bad)
	e.d.Run = e.run
	err := ApplyFPM(context.Background(), e.d, "8.4")
	var failed *configtx.TestFailed
	if !errors.As(err, &failed) || !strings.Contains(err.Error(), "unable to parse value for entry 'pm'") {
		t.Fatalf("ApplyFPM = %v, want a test failure with php-fpm's output", err)
	}
	if got := commands(e.run.Calls()); !slices.Equal(got, []string{line(fpmTest("8.4"))}) {
		t.Errorf("ran %q, want only the test", got)
	}
}

// A dpkg run interrupted by a killed job makes apt-get refuse everything
// until dpkg --configure -a. Install runs that when apt-get says so, then
// tries once more.
func TestInstallRepairsInterruptedDpkg(t *testing.T) {
	e := newEnv(t, fixture(t, "sury-apt.gpg"), "/php/")
	writeFile(t, filepath.Join(e.p.PHPPoolDir("8.4"), "zimam-shop.conf"), "[zimam-shop]\n")
	for _, s := range []sysexec.Script{
		aptUpdate, interrupted(aptInstall("8.4")), dpkgConfigure, aptInstall("8.4"),
		fpmTest("8.4"), systemctlCmd("is-active", "--quiet", "php8.4-fpm"), systemctlCmd("reload", "php8.4-fpm"),
	} {
		e.run.Add(s)
	}
	if err := Install(context.Background(), e.d, "8.4"); err != nil {
		t.Fatalf("Install: %v", err)
	}
	calls := e.run.Calls()
	install := commands([]sysexec.Call{calls[1]})[0]
	if got := commands(calls[1:4]); !slices.Equal(got, []string{install, "/usr/bin/dpkg --force-confdef --force-confold --configure -a", install}) {
		t.Errorf("ran %q, want apt-get install, dpkg --configure -a, apt-get install", got)
	}
	if !slices.Contains(calls[2].Env, "DEBIAN_FRONTEND=noninteractive") {
		t.Errorf("dpkg ran with env %q", calls[2].Env)
	}
}

// Any other apt-get failure comes back as it is, without dpkg.
func TestInstallOtherAptFailureIsNotRepaired(t *testing.T) {
	e := newEnv(t, fixture(t, "sury-apt.gpg"), "/php/")
	failed := aptInstall("8.4")
	failed.Result = sysexec.Result{ExitCode: 100, Stderr: []byte("E: Unable to locate package php8.4-redis\n")}
	e.run.Add(aptUpdate)
	e.run.Add(failed)
	err := Install(context.Background(), e.d, "8.4")
	if err == nil || !strings.Contains(err.Error(), "Unable to locate package") {
		t.Fatalf("Install = %v, want apt-get's error", err)
	}
	for _, c := range commands(e.run.Calls()) {
		if strings.HasPrefix(c, "/usr/bin/dpkg") {
			t.Errorf("ran %q", c)
		}
	}
}
