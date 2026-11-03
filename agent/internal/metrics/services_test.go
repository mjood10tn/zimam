package metrics

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"zimam/agent/contract"
	"zimam/agent/internal/paths"
	"zimam/agent/internal/sysexec"
)

func testDeps(t *testing.T, run sysexec.Runner) sysexec.Deps {
	t.Helper()
	return sysexec.Deps{Paths: paths.Paths{Root: t.TempDir()}, Run: run, Logf: t.Logf, Now: time.Now}
}

func touch(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatal(err)
	}
}

// show is what systemctl show prints for units, one block each, in the
// order asked, with a blank line between blocks.
func show(blocks ...[3]string) []byte {
	var b strings.Builder
	for i, u := range blocks {
		if i > 0 {
			b.WriteString("\n")
		}
		b.WriteString("Id=" + u[0] + "\nActiveState=" + u[1] + "\nUnitFileState=" + u[2] + "\n")
	}
	return []byte(b.String())
}

func showArgs(units ...string) []string {
	return append([]string{"show", "-p", "Id,ActiveState,UnitFileState", "--"}, units...)
}

// alwaysUnits are the units monitored on every server, after nginx and the
// FPM services, then both SSH units, of which one is kept.
var alwaysUnits = []string{"zimam-agent.socket", "zimam-panel-queue.service", "zimam-panel-scheduler.timer", "zimam-maintenance.timer", "certbot.timer", "ssh.socket", "ssh.service"}

// fixedBlocks answer alwaysUnits with ssh.socket enabled.
func fixedBlocks() [][3]string {
	return [][3]string{
		{"zimam-agent.socket", "active", "enabled"},
		{"zimam-panel-queue.service", "active", "enabled"},
		{"zimam-panel-scheduler.timer", "active", "enabled"},
		{"zimam-maintenance.timer", "active", "enabled"},
		{"certbot.timer", "active", "enabled"},
		{"ssh.socket", "active", "enabled"},
		{"ssh.service", "active", "disabled"},
	}
}

// php<v>-fpm is monitored for each version with pools: 8.3 has a site's
// pool and 8.5 the panel's, while 8.4 is installed with no pool, so its FPM
// is stopped on purpose and must not show as a failing service.
func TestServicesIncludePoolVersionsOnly(t *testing.T) {
	units := append([]string{"nginx.service", "php8.3-fpm.service", "php8.5-fpm.service"}, alwaysUnits...)
	blocks := append([][3]string{{"nginx.service", "active", "enabled"}, {"php8.3-fpm.service", "active", "enabled"}, {"php8.5-fpm.service", "failed", "enabled"}}, fixedBlocks()...)
	run := sysexec.NewFakeRunner(sysexec.Script{Path: "/usr/bin/systemctl", Args: showArgs(units...), Result: sysexec.Result{Stdout: show(blocks...)}})
	d := testDeps(t, run)
	touch(t, d.Paths.PHPPool("8.3", "shop"))
	touch(t, d.Paths.PHPGlobalConf("8.4"))
	touch(t, d.Paths.PHPFPMBinary("8.4"))
	touch(t, d.Paths.PanelPool("8.5"))

	got, err := Services(context.Background(), d)
	if err != nil {
		t.Fatal(err)
	}
	want := []contract.ServiceState{
		{Unit: "nginx.service", ActiveState: "active", UnitFileState: "enabled"},
		{Unit: "php8.3-fpm.service", ActiveState: "active", UnitFileState: "enabled"},
		{Unit: "php8.5-fpm.service", ActiveState: "failed", UnitFileState: "enabled"},
		{Unit: "zimam-agent.socket", ActiveState: "active", UnitFileState: "enabled"},
		{Unit: "zimam-panel-queue.service", ActiveState: "active", UnitFileState: "enabled"},
		{Unit: "zimam-panel-scheduler.timer", ActiveState: "active", UnitFileState: "enabled"},
		{Unit: "zimam-maintenance.timer", ActiveState: "active", UnitFileState: "enabled"},
		{Unit: "certbot.timer", ActiveState: "active", UnitFileState: "enabled"},
		{Unit: "ssh.socket", ActiveState: "active", UnitFileState: "enabled"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Services =\n%+v\nwant\n%+v", got, want)
	}
	if calls := run.Calls(); len(calls) != 1 {
		t.Errorf("ran %d commands, want one systemctl show", len(calls))
	}
}

// Ubuntu 26.04 starts sshd from ssh.socket; a server where the admin went
// back to ssh.service has that one enabled instead. Whichever is enabled is
// monitored; with neither, no SSH unit is.
func TestSSHSocketOrService(t *testing.T) {
	for _, tc := range []struct {
		name            string
		socket, service string // UnitFileState
		want            string // "" for none
	}{
		{"socket", "enabled", "disabled", "ssh.socket"},
		{"service", "disabled", "enabled", "ssh.service"},
		{"both enabled", "enabled", "enabled", "ssh.socket"},
		{"service enabled at runtime", "disabled", "enabled-runtime", "ssh.service"},
		{"neither", "disabled", "disabled", ""},
		{"not installed", "", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			blocks := append([][3]string{{"nginx.service", "active", "enabled"}}, fixedBlocks()...)
			blocks[len(blocks)-2][2] = tc.socket
			blocks[len(blocks)-1][2] = tc.service
			units := append([]string{"nginx.service"}, alwaysUnits...)
			run := sysexec.NewFakeRunner(sysexec.Script{Path: "/usr/bin/systemctl", Args: showArgs(units...), Result: sysexec.Result{Stdout: show(blocks...)}})
			got, err := Services(context.Background(), testDeps(t, run))
			if err != nil {
				t.Fatal(err)
			}
			var ssh []string
			for _, s := range got {
				if strings.HasPrefix(s.Unit, "ssh.") {
					ssh = append(ssh, s.Unit)
				}
			}
			want := []string{tc.want}
			if tc.want == "" {
				want = nil
			}
			if !slices.Equal(ssh, want) {
				t.Errorf("SSH units = %v, want %v", ssh, want)
			}
		})
	}
}

// systemctl show answers with properties in its own order, and a unit that
// isn't installed with an empty UnitFileState; either is read as given. An
// answer that doesn't match the units asked for fails, rather than pinning
// one unit's state on another.
func TestServicesParsing(t *testing.T) {
	units := append([]string{"nginx.service"}, alwaysUnits...)
	var b strings.Builder
	b.WriteString("ActiveState=inactive\nUnitFileState=\nId=nginx.service\n")
	for _, u := range fixedBlocks() {
		b.WriteString("\nId=" + u[0] + "\nActiveState=" + u[1] + "\nUnitFileState=" + u[2] + "\n")
	}
	run := sysexec.NewFakeRunner(sysexec.Script{Path: "/usr/bin/systemctl", Args: showArgs(units...), Result: sysexec.Result{Stdout: []byte(b.String())}})
	got, err := Services(context.Background(), testDeps(t, run))
	if err != nil {
		t.Fatal(err)
	}
	if got[0] != (contract.ServiceState{Unit: "nginx.service", ActiveState: "inactive", UnitFileState: ""}) {
		t.Errorf("nginx = %+v", got[0])
	}

	for name, out := range map[string][]byte{
		"a block missing": show(fixedBlocks()...),
		"wrong unit":      show(append([][3]string{{"apache2.service", "active", "enabled"}}, fixedBlocks()...)...),
		"no ActiveState":  []byte("Id=nginx.service\nUnitFileState=enabled\n"),
	} {
		run := sysexec.NewFakeRunner(sysexec.Script{Path: "/usr/bin/systemctl", Args: showArgs(units...), Result: sysexec.Result{Stdout: out}})
		if _, err := Services(context.Background(), testDeps(t, run)); err == nil {
			t.Errorf("%s: Services accepted it", name)
		}
	}
	failing := sysexec.NewFakeRunner(sysexec.Script{Path: "/usr/bin/systemctl", Args: showArgs(units...), Result: sysexec.Result{ExitCode: 1}})
	if _, err := Services(context.Background(), testDeps(t, failing)); err == nil {
		t.Error("Services ignored a failed systemctl")
	}
}
