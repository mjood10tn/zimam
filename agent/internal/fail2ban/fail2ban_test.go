package fail2ban

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"zimam/agent/contract"
	"zimam/agent/internal/paths"
	"zimam/agent/internal/sysexec"
)

const systemctl = "/usr/bin/systemctl"

func params(ignore ...string) contract.Fail2banConfigParams {
	return contract.Fail2banConfigParams{Ignore: contract.IPList(append([]string{}, ignore...)), Jails: []contract.Fail2banJail{
		{Name: "sshd", Enabled: true, MaxRetry: 5, FindTime: 600, BanTime: 3600},
		{Name: "zimam-panel", Enabled: true, MaxRetry: 5, FindTime: 600, BanTime: 3600},
		{Name: "zimam-site-auth", Enabled: false, MaxRetry: 10, FindTime: 600, BanTime: 600},
	}}
}

func call(path string, args ...string) sysexec.Script { return sysexec.Script{Path: path, Args: args} }

func out(s sysexec.Script, stdout string) sysexec.Script {
	s.Result.Stdout = []byte(stdout)
	return s
}

func status(jails string) sysexec.Script {
	return out(call(client, "status"), "Status\n|- Number of jail:\t2\n`- Jail list:\t"+jails+"\n")
}

// newEnv is a server with fail2ban installed.
func newEnv(t *testing.T, scripts ...sysexec.Script) (sysexec.Deps, *sysexec.FakeRunner) {
	t.Helper()
	p := paths.Paths{Root: t.TempDir()}
	for _, dir := range []string{filepath.Dir(p.Fail2banJails()), filepath.Dir(p.Fail2banPanelFilter()), filepath.Dir(p.Fail2banClient())} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(p.Fail2banClient(), nil, 0o755); err != nil {
		t.Fatal(err)
	}
	run := sysexec.NewFakeRunner(scripts...)
	return sysexec.Deps{Paths: p, Run: run, Logf: t.Logf, Now: time.Now}, run
}

func matched(p paths.Paths, n string) sysexec.Script {
	return out(call(regex, sampleLine, p.Fail2banPanelFilter()), "Lines: 1 lines, 0 ignored, "+n+" matched, 0 missed\n")
}

func TestConfigureWritesTestsAndReloads(t *testing.T) {
	d, run := newEnv(t)
	for _, s := range []sysexec.Script{
		call(systemctl, "enable", "--now", "fail2ban.service"), matched(d.Paths, "1"),
		call(client, "-t"), call(client, "reload"), status("sshd, zimam-panel"),
		call(systemctl, "is-active", "--quiet", "fail2ban.service"),
		call(client, "get", "sshd", "banip", "--with-time"), call(client, "get", "zimam-panel", "banip", "--with-time"),
	} {
		run.Add(s)
	}
	res, err := Configure(context.Background(), d, params("203.0.113.0/24"))
	if err != nil {
		t.Fatal(err)
	}
	if !res.Installed || !res.Running || len(res.Jails) != 2 {
		t.Errorf("status = %+v", res)
	}
	b, _ := os.ReadFile(d.Paths.Fail2banJails())
	if !strings.Contains(string(b), "ignoreip = 127.0.0.1/8 ::1 203.0.113.0/24\n") || !strings.Contains(string(b), "[zimam-site-auth]\nenabled = false\n") {
		t.Errorf("zimam.local =\n%s", b)
	}
	if _, err := os.Stat(d.Paths.Fail2banPanelFilter()); err != nil {
		t.Errorf("no panel filter: %v", err)
	}
	for _, c := range run.Calls() {
		if c.Path == "/usr/bin/apt-get" {
			t.Error("installed fail2ban again")
		}
	}
}

// The filter must match a real line: -t alone accepts a broken regex.
func TestFilterThatMatchesNothingRestoresPrevious(t *testing.T) {
	d, run := newEnv(t)
	if err := os.WriteFile(d.Paths.Fail2banJails(), []byte("# before\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, s := range []sysexec.Script{
		call(systemctl, "enable", "--now", "fail2ban.service"),
		matched(d.Paths, "0"), matched(d.Paths, "1"), // the new files, then the old ones
		call(client, "-t"), call(client, "reload"),
	} {
		run.Add(s)
	}
	if _, err := Configure(context.Background(), d, params()); err == nil || !strings.Contains(err.Error(), "doesn't match") {
		t.Fatalf("Configure = %v, want the filter test to fail", err)
	}
	if b, _ := os.ReadFile(d.Paths.Fail2banJails()); string(b) != "# before\n" {
		t.Errorf("zimam.local = %q, want the earlier one", b)
	}
	if _, err := os.Stat(d.Paths.Fail2banPanelFilter()); !os.IsNotExist(err) {
		t.Errorf("the new filter is still there: %v", err)
	}
}

// fail2ban must run exactly the enabled jails, or the old files come back.
func TestMissingJailRestoresPrevious(t *testing.T) {
	d, run := newEnv(t)
	for _, s := range []sysexec.Script{
		call(systemctl, "enable", "--now", "fail2ban.service"), matched(d.Paths, "1"),
		call(client, "-t"), call(client, "reload"), status("sshd"),
	} {
		run.Add(s)
	}
	_, err := Configure(context.Background(), d, params())
	if err == nil || !strings.Contains(err.Error(), "runs the jails [sshd]") {
		t.Fatalf("Configure = %v", err)
	}
	if _, err := os.Stat(d.Paths.Fail2banJails()); !os.IsNotExist(err) {
		t.Errorf("zimam.local left in place: %v", err)
	}
	var reloads int
	for _, c := range run.Calls() {
		if c.Path == client && slices.Equal(c.Args, []string{"reload"}) {
			reloads++
		}
	}
	if reloads != 2 {
		t.Errorf("reloaded %d times, want the new files then the old", reloads)
	}
}

func TestConfigureInstallsOnFirstUse(t *testing.T) {
	d, run := newEnv(t)
	if err := os.Remove(d.Paths.Fail2banClient()); err != nil {
		t.Fatal(err)
	}
	run.Add(sysexec.Script{Path: "/usr/bin/apt-get", AnyArgs: true})
	run.Add(call(systemctl, "enable", "--now", "fail2ban.service"))
	run.Add(matched(d.Paths, "0"))
	run.Add(call(client, "-t"))
	run.Add(call(client, "reload"))
	_, _ = Configure(context.Background(), d, params())
	var installs []string
	for _, c := range run.Calls() {
		if c.Path == "/usr/bin/apt-get" && slices.Contains(c.Args, "install") {
			installs = append(installs, c.Args[len(c.Args)-1])
		}
	}
	if !slices.Equal(installs, []string{"fail2ban"}) {
		t.Errorf("installed %v, want fail2ban", installs)
	}
}

func TestIgnoreListRendersParsedValues(t *testing.T) {
	p := params("192.0.2.7", "2001:db8::/32")
	if err := p.Validate(); err != nil {
		t.Fatal(err)
	}
	b, err := renderJails(p)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "ignoreip = 127.0.0.1/8 ::1 192.0.2.7/32 2001:db8::/32\n") {
		t.Errorf("zimam.local =\n%s", b)
	}
	if _, err := renderJails(params("192.0.2.7/24")); err == nil {
		t.Error("rendered an ignore entry with host bits")
	}
}

func TestStatusParsesBans(t *testing.T) {
	d, _ := newEnv(t,
		call(systemctl, "is-active", "--quiet", "fail2ban.service"),
		status("sshd, zimam-panel, recidive"),
		out(call(client, "get", "sshd", "banip", "--with-time"), "192.0.2.50 \t2026-11-24 05:29:06 + 600 = 2026-11-24 05:39:06\n2001:db8::50 \t2026-11-24 05:29:06 + 600 = 2026-11-24 05:39:06\n"),
		out(call(client, "get", "zimam-panel", "banip", "--with-time"), "\n"),
	)
	res, err := Status(context.Background(), d)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Jails) != 2 || res.Jails[0].Name != "sshd" || len(res.Jails[0].Banned) != 2 || len(res.Jails[1].Banned) != 0 {
		t.Fatalf("status = %+v", res)
	}
	want := time.Date(2026, 11, 24, 5, 39, 6, 0, time.Local).UTC()
	if b := res.Jails[0].Banned[1]; b.Address != "2001:db8::50" || !b.Until.Equal(want) {
		t.Errorf("ban = %+v, want until %v", b, want)
	}
}

func TestStatusWhenNotInstalledOrStopped(t *testing.T) {
	d, _ := newEnv(t)
	if err := os.Remove(d.Paths.Fail2banClient()); err != nil {
		t.Fatal(err)
	}
	if res, err := Status(context.Background(), d); err != nil || res.Installed || res.Running {
		t.Errorf("Status = %+v, %v", res, err)
	}
	stopped := call(systemctl, "is-active", "--quiet", "fail2ban.service")
	stopped.Result.ExitCode = 3
	d, _ = newEnv(t, stopped)
	if res, err := Status(context.Background(), d); err != nil || !res.Installed || res.Running {
		t.Errorf("Status = %+v, %v", res, err)
	}
}

func TestUnban(t *testing.T) {
	d, run := newEnv(t, out(call(client, "set", "zimam-panel", "unbanip", "192.0.2.50"), "1\n"))
	if ok, err := Unban(context.Background(), d, "zimam-panel", "192.0.2.50"); err != nil || !ok {
		t.Errorf("Unban = %v, %v", ok, err)
	}
	run.Add(out(call(client, "set", "sshd", "unbanip", "192.0.2.99"), "0\n"))
	if ok, err := Unban(context.Background(), d, "sshd", "192.0.2.99"); err != nil || ok {
		t.Errorf("Unban of an address that wasn't banned = %v, %v", ok, err)
	}
}
