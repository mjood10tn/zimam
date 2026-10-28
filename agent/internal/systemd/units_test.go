package systemd

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"slices"
	"strings"
	"testing"

	"zimam/agent/internal/configtx"
	"zimam/agent/internal/paths"
	"zimam/agent/internal/sysexec"
)

func commands(run *sysexec.FakeRunner) []string {
	var lines []string
	for _, c := range run.Calls() {
		lines = append(lines, strings.Join(append([]string{c.Path}, c.Args...), " "))
	}
	return lines
}

func newDeps(t *testing.T, scripts ...sysexec.Script) (sysexec.Deps, *sysexec.FakeRunner) {
	t.Helper()
	p := paths.Paths{Root: t.TempDir()}
	if err := os.MkdirAll(p.Root+"/etc/systemd/system", 0o755); err != nil {
		t.Fatal(err)
	}
	run := sysexec.NewFakeRunner(scripts...)
	return sysexec.Deps{Paths: p, Run: run, Logf: t.Logf}, run
}

func verify(p paths.Paths, names ...string) sysexec.Script {
	args := []string{"verify"}
	for _, n := range names {
		args = append(args, p.SystemdUnit(n))
	}
	return sysexec.Script{Path: "/usr/bin/systemd-analyze", Args: args}
}

func systemctlCmd(args ...string) sysexec.Script {
	return sysexec.Script{Path: "/usr/bin/systemctl", Args: args}
}

func line(s sysexec.Script) string { return strings.Join(append([]string{s.Path}, s.Args...), " ") }

var maintenance = []Unit{
	{Name: "zimam-maintenance.service", Content: []byte("[Service]\nType=oneshot\n")},
	{Name: "zimam-maintenance.timer", Content: []byte("[Timer]\nOnCalendar=*:0/15\n"), Enable: true},
}

func TestInstallUnitsReloadsAndEnables(t *testing.T) {
	d, run := newDeps(t)
	v := verify(d.Paths, "zimam-maintenance.service", "zimam-maintenance.timer")
	run.Add(v)
	run.Add(systemctlCmd("daemon-reload"))
	run.Add(systemctlCmd("enable", "--now", "zimam-maintenance.timer"))
	if err := InstallUnits(context.Background(), d, maintenance); err != nil {
		t.Fatalf("InstallUnits: %v", err)
	}
	for _, u := range maintenance {
		b, err := os.ReadFile(d.Paths.SystemdUnit(u.Name))
		if err != nil || string(b) != string(u.Content) {
			t.Errorf("%s = %q, %v", u.Name, b, err)
		}
		if fi, err := os.Stat(d.Paths.SystemdUnit(u.Name)); err != nil {
			t.Error(err)
		} else if fi.Mode().Perm() != 0o644 {
			t.Errorf("%s: mode %v, want 0644", u.Name, fi.Mode().Perm())
		}
	}
	// A timer-run service is started by its timer, so only the timer is
	// enabled.
	want := []string{line(v), "/usr/bin/systemctl daemon-reload", "/usr/bin/systemctl enable --now zimam-maintenance.timer"}
	if got := commands(run); !slices.Equal(got, want) {
		t.Errorf("ran %q, want %q", got, want)
	}
}

func TestInstallUnitsVerifyFailureRestores(t *testing.T) {
	d, run := newDeps(t)
	old := "[Service]\nType=oneshot\nExecStart=/usr/local/sbin/zimam-agent maintenance\n"
	if err := os.WriteFile(d.Paths.SystemdUnit("zimam-maintenance.service"), []byte(old), 0o644); err != nil {
		t.Fatal(err)
	}
	bad := verify(d.Paths, "zimam-maintenance.service", "zimam-maintenance.timer")
	bad.Result = sysexec.Result{ExitCode: 1, Stderr: []byte("zimam-maintenance.timer: Timer unit lacks value setting. Refusing.\n")}
	run.Add(bad)
	run.Add(verify(d.Paths, "zimam-maintenance.service", "zimam-maintenance.timer")) // the retest
	err := InstallUnits(context.Background(), d, maintenance)
	var failed *configtx.TestFailed
	if !errors.As(err, &failed) || !strings.Contains(err.Error(), "Timer unit lacks value setting") {
		t.Fatalf("InstallUnits = %v, want a test failure with the verify output", err)
	}
	if b, _ := os.ReadFile(d.Paths.SystemdUnit("zimam-maintenance.service")); string(b) != old {
		t.Errorf("the service = %q, want the previous one back", b)
	}
	if _, err := os.Lstat(d.Paths.SystemdUnit("zimam-maintenance.timer")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the new timer is still there (Lstat: %v)", err)
	}
	for _, c := range commands(run) {
		if strings.HasPrefix(c, "/usr/bin/systemctl") {
			t.Errorf("ran %q after a failed verify", c)
		}
	}
}

func TestInstallUnitsRefusesBadNames(t *testing.T) {
	for _, name := range []string{"../zimam-x.service", "zimam-x.conf", "sshd.service", "zimam-x.service\n", ""} {
		d, run := newDeps(t)
		err := InstallUnits(context.Background(), d, []Unit{{Name: name, Content: []byte("[Service]\n")}})
		if err == nil {
			t.Errorf("InstallUnits accepted %q", name)
		}
		if got := commands(run); len(got) != 0 {
			t.Errorf("%q: ran %q", name, got)
		}
	}
}

func TestZimamUnits(t *testing.T) {
	units, err := ZimamUnits("8.4")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, u := range units {
		got = append(got, u.Name)
		if u.Enable != (u.Name == "zimam-panel-queue.service" || strings.HasSuffix(u.Name, ".timer")) {
			t.Errorf("%s: Enable = %v", u.Name, u.Enable)
		}
		if len(u.Content) == 0 {
			t.Errorf("%s is empty", u.Name)
		}
	}
	want := []string{"zimam-panel-queue.service", "zimam-panel-scheduler.service", "zimam-panel-scheduler.timer", "zimam-maintenance.service", "zimam-maintenance.timer"}
	if !slices.Equal(got, want) {
		t.Errorf("units %q, want %q", got, want)
	}
	if !strings.Contains(string(units[0].Content), "ExecStart=/usr/bin/php8.4 artisan queue:work") {
		t.Errorf("the queue runs another PHP:\n%s", units[0].Content)
	}
	if _, err := ZimamUnits("8.1"); err == nil {
		t.Error("ZimamUnits accepted PHP 8.1")
	}
}
