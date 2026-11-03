package commands_test

import (
	"context"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"zimam/agent/contract"
	"zimam/agent/internal/commands"
	"zimam/agent/internal/config"
	"zimam/agent/internal/jobs"
	"zimam/agent/internal/paths"
	"zimam/agent/internal/php"
	"zimam/agent/internal/sysexec"
)

// phpEnv is a server whose agent.json makes 8.4 the default and 8.5 the
// panel's version, with 8.3, 8.4 and 8.5 installed. The clock reads
// 2026-11-03, before 8.2's end of support.
type phpEnv struct {
	p   paths.Paths
	run *sysexec.FakeRunner
	d   sysexec.Deps
	now time.Time
}

func newPHPEnv(t *testing.T) *phpEnv {
	t.Helper()
	e := &phpEnv{p: paths.Paths{Root: t.TempDir()}, run: sysexec.NewFakeRunner(), now: time.Date(2026, 11, 3, 9, 0, 0, 0, time.UTC)}
	if err := config.Save(e.p, config.New("panel.example.com", "8.4", "8.5")); err != nil {
		t.Fatal(err)
	}
	for _, v := range []contract.PHPVersion{"8.3", "8.4", "8.5"} {
		touch(t, e.p.PHPFPMBinary(v))
		touch(t, e.p.PHPGlobalConf(v))
	}
	touch(t, e.p.PanelPool("8.5"))
	e.d = sysexec.Deps{Paths: e.p, Run: e.run, Logf: t.Logf, Now: func() time.Time { return e.now }}
	return e
}

func (e *phpEnv) precheck(t *testing.T, command string, v contract.PHPVersion) error {
	t.Helper()
	pre := commands.Prechecks(e.d)[command]
	if pre == nil {
		t.Fatalf("%s has no precheck", command)
	}
	return pre(context.Background(), &contract.PHPVersionParams{Version: v})
}

// handle runs a job command's handler as job run would, with the config on
// disk and the fake runner.
func (e *phpEnv) handle(t *testing.T, command string, v contract.PHPVersion) (any, error) {
	t.Helper()
	cfg, err := config.Load(e.p)
	if err != nil {
		t.Fatal(err)
	}
	h := commands.JobHandlers()[command]
	if h == nil {
		t.Fatalf("%s has no job handler", command)
	}
	j := &jobs.Context{ID: "01JBX7Q3A1B2C3D4E5F6G7H8J9", Logf: t.Logf, Config: cfg, Paths: e.p, Run: e.run}
	return h(context.Background(), j, &contract.PHPVersionParams{Version: v})
}

func wantCode(t *testing.T, err error, code contract.ErrorCode) *contract.AgentError {
	t.Helper()
	var ae *contract.AgentError
	if !errors.As(err, &ae) || ae.Code != code {
		t.Fatalf("err = %v, want %s", err, code)
	}
	return ae
}

func (e *phpEnv) calls() []string {
	var out []string
	for _, c := range e.run.Calls() {
		out = append(out, strings.Join(append([]string{c.Path}, c.Args...), " "))
	}
	return out
}

func TestInstallPrecheckPastEOL(t *testing.T) {
	e := newPHPEnv(t)
	if err := e.precheck(t, "php.install", "8.2"); err != nil {
		t.Fatalf("on 2026-11-03, installing 8.2: %v, want allowed", err)
	}
	e.now = time.Date(2026, 12, 31, 23, 59, 0, 0, time.UTC)
	if err := e.precheck(t, "php.install", "8.2"); err != nil {
		t.Fatalf("on its last day, installing 8.2: %v, want allowed", err)
	}
	e.now = time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	ae := wantCode(t, e.precheck(t, "php.install", "8.2"), contract.ErrValidationFailed)
	if !strings.Contains(ae.Fields["version"], "2026-12-31") {
		t.Errorf("fields = %v, want version to name the end date", ae.Fields)
	}
	if len(e.run.Calls()) != 0 {
		t.Errorf("the precheck ran %q", e.calls())
	}
}

func TestInstallAlreadyInstalledConflict(t *testing.T) {
	e := newPHPEnv(t)
	wantCode(t, e.precheck(t, "php.install", "8.3"), contract.ErrConflict)
	if err := e.precheck(t, "php.install", "8.2"); err != nil {
		t.Errorf("installing 8.2, not installed: %v", err)
	}
}

func TestUninstallConflictPanel(t *testing.T) {
	e := newPHPEnv(t)
	ae := wantCode(t, e.precheck(t, "php.uninstall", "8.5"), contract.ErrConflict)
	if !strings.Contains(ae.Message, "panel") {
		t.Errorf("message = %q, want it to name the panel", ae.Message)
	}
}

func TestUninstallConflictDefault(t *testing.T) {
	e := newPHPEnv(t)
	ae := wantCode(t, e.precheck(t, "php.uninstall", "8.4"), contract.ErrConflict)
	if !strings.Contains(ae.Message, "default") {
		t.Errorf("message = %q, want it to name the default", ae.Message)
	}
}

// The precheck reads agent.json on each call: php.default changes it while
// serve runs.
func TestUninstallConflictDefaultReadsConfigEachCall(t *testing.T) {
	e := newPHPEnv(t)
	if err := e.precheck(t, "php.uninstall", "8.3"); err != nil {
		t.Fatalf("uninstalling 8.3: %v", err)
	}
	cfg := config.New("panel.example.com", "8.3", "8.5")
	if err := config.Save(e.p, cfg); err != nil {
		t.Fatal(err)
	}
	wantCode(t, e.precheck(t, "php.uninstall", "8.3"), contract.ErrConflict)
}

func TestUninstallConflictSitePool(t *testing.T) {
	e := newPHPEnv(t)
	touch(t, e.p.PHPPool("8.3", "shop"))
	ae := wantCode(t, e.precheck(t, "php.uninstall", "8.3"), contract.ErrConflict)
	if !strings.Contains(strings.ToLower(ae.Message), "site") {
		t.Errorf("message = %q, want it to name the sites", ae.Message)
	}
}

func TestUninstallNotInstalled(t *testing.T) {
	e := newPHPEnv(t)
	wantCode(t, e.precheck(t, "php.uninstall", "8.2"), contract.ErrNotFound)
}

func TestUninstallSequence(t *testing.T) {
	e := newPHPEnv(t)
	e.run.Add(sysexec.Script{Path: "/usr/bin/systemctl", Args: []string{"disable", "--now", "php8.3-fpm"}})
	purge := append([]string{"-o", "DPkg::Lock::Timeout=300", "purge", "-y"}, php.Packages("8.3")...)
	e.run.Add(sysexec.Script{Path: "/usr/bin/apt-get", Args: purge})
	res, err := e.handle(t, "php.uninstall", "8.3")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"/usr/bin/systemctl disable --now php8.3-fpm",
		"/usr/bin/apt-get " + strings.Join(purge, " "),
	}
	if got := e.calls(); !slices.Equal(got, want) {
		t.Errorf("calls =\n%q\nwant\n%q", got, want)
	}
	for _, c := range e.run.Calls() {
		if c.Path == "/usr/bin/apt-get" && !slices.Contains(c.Env, "DEBIAN_FRONTEND=noninteractive") {
			t.Errorf("apt-get env = %q, want DEBIAN_FRONTEND=noninteractive", c.Env)
		}
	}
	if res != (contract.PHPVersionResult{Version: "8.3"}) {
		t.Errorf("result = %+v", res)
	}
}

// The handler checks again before removing anything: a site may have got a
// pool on this version since the job was accepted.
func TestUninstallHandlerRechecks(t *testing.T) {
	e := newPHPEnv(t)
	touch(t, e.p.PHPPool("8.3", "shop"))
	_, err := e.handle(t, "php.uninstall", "8.3")
	wantCode(t, err, contract.ErrConflict)
	if len(e.run.Calls()) != 0 {
		t.Errorf("ran %q", e.calls())
	}
}

// A job killed while dpkg ran (its time limit, or out of memory) leaves dpkg
// interrupted, and apt-get then refuses to do anything. When apt-get says
// so, the next PHP job finishes dpkg's work and tries again.
func TestUninstallRepairsInterruptedDpkg(t *testing.T) {
	e := newPHPEnv(t)
	e.run.Add(sysexec.Script{Path: "/usr/bin/systemctl", Args: []string{"disable", "--now", "php8.3-fpm"}})
	purge := append([]string{"-o", "DPkg::Lock::Timeout=300", "purge", "-y"}, php.Packages("8.3")...)
	e.run.Add(sysexec.Script{Path: "/usr/bin/apt-get", Args: purge, Result: sysexec.Result{
		ExitCode: 100,
		Stderr:   []byte("E: dpkg was interrupted, you must manually run 'dpkg --configure -a' to correct the problem. \n"),
	}})
	e.run.Add(sysexec.Script{Path: "/usr/bin/dpkg", Args: []string{"--force-confdef", "--force-confold", "--configure", "-a"}})
	e.run.Add(sysexec.Script{Path: "/usr/bin/apt-get", Args: purge})
	if _, err := e.handle(t, "php.uninstall", "8.3"); err != nil {
		t.Fatal(err)
	}
	purgeCmd := "/usr/bin/apt-get " + strings.Join(purge, " ")
	want := []string{"/usr/bin/systemctl disable --now php8.3-fpm", purgeCmd, "/usr/bin/dpkg --force-confdef --force-confold --configure -a", purgeCmd}
	if got := e.calls(); !slices.Equal(got, want) {
		t.Errorf("calls =\n%q\nwant\n%q", got, want)
	}
}

func TestInstallRunsPHPInstall(t *testing.T) {
	e := newPHPEnv(t)
	// php.Install's own tests cover its steps; here the repository
	// download fails at once, which shows the handler called it.
	cfg, _ := config.Load(e.p)
	cfg.PHPRepoURL = "https://127.0.0.1:1/php/"
	if err := config.Save(e.p, cfg); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(e.p.LocksDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	_, err := e.handle(t, "php.install", "8.2")
	if err == nil || !strings.Contains(err.Error(), "127.0.0.1:1") {
		t.Errorf("err = %v, want the repository download's failure", err)
	}
}

func alternatives(v string, phar, pharPhar bool) []sysexec.Script {
	query := func(name string, registered bool) sysexec.Script {
		s := sysexec.Script{Path: "/usr/bin/update-alternatives", Args: []string{"--query", name}}
		if !registered {
			s.Result.ExitCode = 2
		}
		return s
	}
	return []sysexec.Script{
		{Path: "/usr/bin/update-alternatives", Args: []string{"--set", "php", "/usr/bin/php" + v}},
		query("phar", phar),
		{Path: "/usr/bin/update-alternatives", Args: []string{"--set", "phar", "/usr/bin/phar" + v}},
		query("phar.phar", pharPhar),
		{Path: "/usr/bin/update-alternatives", Args: []string{"--set", "phar.phar", "/usr/bin/phar.phar" + v}},
	}
}

func TestDefaultSetsAlternatives(t *testing.T) {
	e := newPHPEnv(t)
	for _, s := range alternatives("8.3", true, true) {
		e.run.Add(s)
	}
	if _, err := e.handle(t, "php.default", "8.3"); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"/usr/bin/update-alternatives --set php /usr/bin/php8.3",
		"/usr/bin/update-alternatives --query phar",
		"/usr/bin/update-alternatives --set phar /usr/bin/phar8.3",
		"/usr/bin/update-alternatives --query phar.phar",
		"/usr/bin/update-alternatives --set phar.phar /usr/bin/phar.phar8.3",
	}
	if got := e.calls(); !slices.Equal(got, want) {
		t.Errorf("calls =\n%q\nwant\n%q", got, want)
	}
}

func TestDefaultSkipsUnregisteredPhar(t *testing.T) {
	e := newPHPEnv(t)
	for _, s := range alternatives("8.3", false, true) {
		e.run.Add(s)
	}
	if _, err := e.handle(t, "php.default", "8.3"); err != nil {
		t.Fatal(err)
	}
	got := e.calls()
	if slices.Contains(got, "/usr/bin/update-alternatives --set phar /usr/bin/phar8.3") {
		t.Errorf("set phar, which isn't registered: %q", got)
	}
	if !slices.Contains(got, "/usr/bin/update-alternatives --set php /usr/bin/php8.3") ||
		!slices.Contains(got, "/usr/bin/update-alternatives --set phar.phar /usr/bin/phar.phar8.3") {
		t.Errorf("calls = %q, want php and phar.phar set", got)
	}
}

// php.default stores default_php and changes nothing else: existing sites
// keep their pools and vhosts (§8.5).
func TestDefaultStoresConfigAndLeavesPools(t *testing.T) {
	e := newPHPEnv(t)
	touch(t, e.p.PHPPool("8.4", "shop"))
	touch(t, e.p.NginxSiteAvailable("shop"))
	before := snapshot(t, e.p.PHPPool("8.4", "shop"), e.p.NginxSiteAvailable("shop"), e.p.PanelPool("8.5"))
	for _, s := range alternatives("8.3", true, true) {
		e.run.Add(s)
	}
	res, err := e.handle(t, "php.default", "8.3")
	if err != nil {
		t.Fatal(err)
	}
	if res != (contract.PHPVersionResult{Version: "8.3"}) {
		t.Errorf("result = %+v", res)
	}
	cfg, err := config.Load(e.p)
	if err != nil {
		t.Fatal(err)
	}
	want := config.New("panel.example.com", "8.3", "8.5")
	if cfg != want {
		t.Errorf("agent.json = %+v, want only default_php changed: %+v", cfg, want)
	}
	if after := snapshot(t, e.p.PHPPool("8.4", "shop"), e.p.NginxSiteAvailable("shop"), e.p.PanelPool("8.5")); !slices.Equal(after, before) {
		t.Errorf("pools or vhosts changed: %v, then %v", before, after)
	}
	for _, c := range e.calls() {
		if !strings.HasPrefix(c, "/usr/bin/update-alternatives ") {
			t.Errorf("ran %q; php.default only sets alternatives", c)
		}
	}
}

func snapshot(t *testing.T, files ...string) []string {
	t.Helper()
	var out []string
	for _, f := range files {
		fi, err := os.Stat(f)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, f+" "+fi.ModTime().String())
	}
	return out
}

// A version that isn't installed can't become the default:
// update-alternatives would fail on a missing binary.
func TestDefaultNotInstalled(t *testing.T) {
	e := newPHPEnv(t)
	wantCode(t, e.precheck(t, "php.default", "8.2"), contract.ErrNotFound)
	if err := e.precheck(t, "php.default", "8.3"); err != nil {
		t.Errorf("making 8.3 the default: %v", err)
	}
	_, err := e.handle(t, "php.default", "8.2")
	wantCode(t, err, contract.ErrNotFound)
	if len(e.run.Calls()) != 0 {
		t.Errorf("ran %q", e.calls())
	}
}

func TestPhpCommandsAuditedAndLimited(t *testing.T) {
	for name, want := range map[string]struct {
		subject string
		limits  jobs.Limits
	}{
		"php.install":   {"php:8.4", jobs.Limits{MemoryMax: "1G", RuntimeMaxSec: 900}},
		"php.uninstall": {"php:8.4", jobs.Limits{MemoryMax: "1G", RuntimeMaxSec: 900}},
		"php.default":   {"server", jobs.Limits{MemoryMax: "256M", RuntimeMaxSec: 120}},
	} {
		d, ok := contract.Lookup(name)
		if !ok {
			t.Errorf("%s isn't registered", name)
			continue
		}
		if d.Kind != contract.KindJob || !d.Audited {
			t.Errorf("%s: kind %s, audited %v; want an audited job", name, d.Kind, d.Audited)
		}
		if got := d.Subject(&contract.PHPVersionParams{Version: "8.4"}); got != want.subject {
			t.Errorf("%s: subject %q, want %q", name, got, want.subject)
		}
		if l, ok := jobs.LimitsFor(name); !ok || l != want.limits {
			t.Errorf("%s: limits %+v, want %+v", name, l, want.limits)
		}
		if commands.JobHandlers()[name] == nil {
			t.Errorf("%s has no job handler", name)
		}
		if commands.Prechecks(sysexec.Deps{})[name] == nil {
			t.Errorf("%s has no precheck", name)
		}
		p := d.Params()
		if err := p.Validate(); err == nil {
			t.Errorf("%s accepts an empty version", name)
		}
		if err := (&contract.PHPVersionParams{Version: "7.4"}).Validate(); err == nil {
			t.Errorf("%s accepts 7.4, outside the allowlist", name)
		}
	}
}
