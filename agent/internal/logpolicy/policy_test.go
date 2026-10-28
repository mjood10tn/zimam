package logpolicy

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"zimam/agent/contract"
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

func newDeps(t *testing.T) (sysexec.Deps, *sysexec.FakeRunner) {
	t.Helper()
	p := paths.Paths{Root: t.TempDir()}
	if err := os.MkdirAll(p.ConfigDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	run := sysexec.NewFakeRunner()
	return sysexec.Deps{Paths: p, Run: run, Logf: t.Logf}, run
}

func logrotateTest(p paths.Paths) sysexec.Script {
	return sysexec.Script{Path: "/usr/sbin/logrotate", Args: []string{"-d", "--state", p.LogrotateState(), p.LogrotateConf()}}
}

var restartJournald = sysexec.Script{Path: "/usr/bin/systemctl", Args: []string{"restart", "systemd-journald"}}

func line(s sysexec.Script) string { return strings.Join(append([]string{s.Path}, s.Args...), " ") }

var policy = Policy{RetentionDays: 30, SiteCapBytes: contract.SiteCapBytes(5 * contract.GiB)}

func files(p paths.Paths, sites ...contract.SiteName) []string {
	all := []string{p.LogrotateConf(), p.OwnLogrotateConf(), p.JournaldDropIn()}
	for _, s := range sites {
		all = append(all, p.SiteLogrotateConf(s))
	}
	return all
}

func TestPolicyRestartsJournald(t *testing.T) {
	d, run := newDeps(t)
	run.Add(logrotateTest(d.Paths))
	run.Add(restartJournald)
	if err := Apply(context.Background(), d, policy, []contract.SiteName{"shop", "blog"}); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	for _, path := range files(d.Paths, "shop", "blog") {
		if fi, err := os.Stat(path); err != nil {
			t.Error(err)
		} else if fi.Mode().Perm() != 0o644 {
			t.Errorf("%s: mode %v, want 0644", path, fi.Mode().Perm())
		}
	}
	for path, want := range map[string]string{
		d.Paths.SiteLogrotateConf("shop"): "/var/log/zimam/sites/shop/*.log {",
		d.Paths.SiteLogrotateConf("blog"): "    su s_blog s_blog\n",
		d.Paths.OwnLogrotateConf():        "    su zimam zimam\n",
		d.Paths.JournaldDropIn():          "MaxRetentionSec=30day\n",
		d.Paths.LogrotateConf():           "include /etc/zimam/logrotate.d\n",
	} {
		if b, _ := os.ReadFile(path); !strings.Contains(string(b), want) || (strings.Contains(path, "logrotate.d") && !strings.Contains(string(b), "maxage 30\n")) {
			t.Errorf("%s lacks %q or maxage 30:\n%s", filepath.Base(path), want, b)
		}
	}
	if got, want := commands(run), []string{line(logrotateTest(d.Paths)), line(restartJournald)}; !slices.Equal(got, want) {
		t.Errorf("ran %q, want %q", got, want)
	}
}

func TestPolicyRollbackOnLogrotateFailure(t *testing.T) {
	d, run := newDeps(t)
	for _, path := range files(d.Paths, "shop") {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("previous\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	bad := logrotateTest(d.Paths)
	bad.Result = sysexec.Result{ExitCode: 1, Stderr: []byte("error: site-shop.conf:3 unknown user 's_shop'\n")}
	run.Add(bad)
	run.Add(logrotateTest(d.Paths)) // the retest
	err := Apply(context.Background(), d, policy, []contract.SiteName{"shop"})
	var failed *configtx.TestFailed
	if !errors.As(err, &failed) || !strings.Contains(err.Error(), "unknown user 's_shop'") {
		t.Fatalf("Apply = %v, want a test failure with logrotate's output", err)
	}
	for _, path := range files(d.Paths, "shop") {
		if b, _ := os.ReadFile(path); string(b) != "previous\n" {
			t.Errorf("%s = %q, want the previous file back", filepath.Base(path), b)
		}
	}
	if slices.Contains(commands(run), line(restartJournald)) {
		t.Error("journald was restarted after a failed test")
	}
}

func TestPolicyRefusesInvalidValues(t *testing.T) {
	for name, c := range map[string]struct {
		p     Policy
		sites []contract.SiteName
	}{
		"no retention": {Policy{RetentionDays: 0, SiteCapBytes: policy.SiteCapBytes}, nil},
		"no cap":       {Policy{RetentionDays: 14}, nil},
		"bad site":     {policy, []contract.SiteName{"../shop"}},
	} {
		d, run := newDeps(t)
		if err := Apply(context.Background(), d, c.p, c.sites); err == nil {
			t.Errorf("%s: Apply succeeded", name)
		}
		if got := commands(run); len(got) != 0 {
			t.Errorf("%s: ran %q", name, got)
		}
		if _, err := os.Stat(d.Paths.LogrotateConf()); err == nil {
			t.Errorf("%s: wrote logrotate.conf", name)
		}
	}
}
