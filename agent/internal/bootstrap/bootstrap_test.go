package bootstrap

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"zimam/agent/contract"
	"zimam/agent/internal/config"
	"zimam/agent/internal/release"
	"zimam/agent/internal/sysexec"
)

func phpV(s string) contract.PHPVersion { return contract.PHPVersion(s) }

var designOrder = []string{
	"agent-config", "directories", "sury-repository", "php", "nginx", "panel-release",
	"panel-site", "panel-certificate", "units", "log-policy", "setup-link",
}

func TestStepOrderMatchesDesign(t *testing.T) {
	if got := StepNames(); !slices.Equal(got, designOrder) {
		t.Fatalf("StepNames() = %q, want §4's order %q", got, designOrder)
	}
	w := newWorld(t)
	w.mustRun(opts())
	if got := w.auditedSteps(0); !slices.Equal(got, designOrder) {
		t.Fatalf("audited steps = %q, want %q", got, designOrder)
	}
}

func TestRerunAppliesNothing(t *testing.T) {
	w := newWorld(t)
	w.mustRun(opts())
	entries, events, requests := len(w.auditEntries()), w.eventCount(), w.http.count()

	if link := w.mustRun(opts()); link != testLink {
		t.Fatalf("the re-run returned %q, want a new setup link", link)
	}
	if got := w.auditedSteps(entries); !slices.Equal(got, []string{"setup-link"}) {
		t.Fatalf("the re-run applied %q, want only setup-link", got)
	}
	if n := w.http.count() - requests; n != 0 {
		t.Fatalf("the re-run made %d downloads", n)
	}
	for _, e := range w.eventsSince(events) {
		reads := strings.HasPrefix(e, "helper stat ") ||
			strings.HasPrefix(e, "run /usr/bin/systemctl is-") ||
			strings.Contains(e, "zimam:setup-link")
		if !reads {
			t.Errorf("the re-run changed something: %s", e)
		}
	}
}

func TestResumeAfterCertFailure(t *testing.T) {
	w := newWorld(t)
	w.sys.fail = func(c sysexec.Cmd) error {
		if c.Path == "/usr/bin/certbot" {
			return &sysexec.ExitError{Path: c.Path, Code: 1, Stderr: []byte("Timeout during connect")}
		}
		return nil
	}
	_, err := w.b.Run(context.Background(), opts())
	var se *StepError
	if !errors.As(err, &se) || se.Step != "panel-certificate" {
		t.Fatalf("first run: %v, want a failure at panel-certificate", err)
	}
	if got, want := w.auditedSteps(0), append(slices.Clone(designOrder[:7]), "panel-certificate error:internal"); !slices.Equal(got, want) {
		t.Fatalf("first run audited %q, want %q", got, want)
	}

	w.sys.fail = nil
	entries := len(w.auditEntries())
	w.mustRun(opts())
	if got := w.auditedSteps(entries); !slices.Equal(got, designOrder[7:]) {
		t.Fatalf("second run applied %q, want %q (steps 1–7 skipped)", got, designOrder[7:])
	}
}

func TestPanelPHPChoice(t *testing.T) {
	for _, tc := range []struct {
		name               string
		php                contract.PHPVersion
		installs           []string
		defaultPHP, panelV contract.PHPVersion
	}{
		{"no --php", "", []string{"8.5"}, "8.5", "8.5"},
		{"--php 8.3", "8.3", []string{"8.5", "8.3"}, "8.3", "8.5"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := newWorld(t)
			o := opts()
			o.PHP = tc.php
			w.mustRun(o)
			var installs, defaults []string
			for _, c := range w.sys.commands() {
				if c.Path == "/usr/bin/apt-get" && slices.Contains(c.Args, "install") {
					for _, a := range c.Args {
						if m := fpmPackage.FindStringSubmatch(a); m != nil {
							installs = append(installs, m[1])
						}
					}
				}
				if c.Path == "/usr/bin/update-alternatives" && c.Args[0] == "--set" && c.Args[1] == "php" {
					defaults = append(defaults, c.Args[2])
				}
			}
			if !slices.Equal(installs, tc.installs) {
				t.Errorf("installed PHP %q, want %q", installs, tc.installs)
			}
			if want := []string{"/usr/bin/php" + string(tc.defaultPHP)}; !slices.Equal(defaults, want) {
				t.Errorf("update-alternatives --set php %q, want %q", defaults, want)
			}
			cfg, err := config.Load(w.p)
			if err != nil {
				t.Fatal(err)
			}
			if cfg.DefaultPHP != tc.defaultPHP || cfg.PanelPHP != tc.panelV {
				t.Errorf("agent.json default_php %s, panel_php %s; want %s and %s", cfg.DefaultPHP, cfg.PanelPHP, tc.defaultPHP, tc.panelV)
			}
		})
	}
}

func TestPHPPastEndOfLifeRefused(t *testing.T) {
	w := newWorld(t)
	w.b.Deps.Now = func() time.Time { return time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC) }
	w.publish(w.priv, panelTarball(t), func(m *release.Manifest) { m.Expires = time.Date(2027, 2, 1, 0, 0, 0, 0, time.UTC) })
	o := opts()
	o.PHP = "8.2" // security support ended on 2026-12-31
	_, err := w.b.Run(context.Background(), o)
	var se *StepError
	if !errors.As(err, &se) || se.Step != "agent-config" || !strings.Contains(err.Error(), "8.2") {
		t.Fatalf("got %v, want agent-config to refuse PHP 8.2", err)
	}
}

func TestEnvContentOnlyOnStdin(t *testing.T) {
	w := newWorld(t)
	w.mustRun(opts())
	env := string(w.help.writes["panel/shared/.env"])
	key := regexp.MustCompile(`(?m)^APP_KEY=(base64:[A-Za-z0-9+/]{43}=)$`).FindStringSubmatch(env)
	if key == nil {
		t.Fatalf(".env has no generated APP_KEY:\n%s", env)
	}
	for _, line := range []string{"APP_ENV=production", "APP_DEBUG=false", "APP_URL=https://panel.example.com", "DB_CONNECTION=sqlite"} {
		if !regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(line) + `$`).MatchString(env) {
			t.Errorf(".env has no line %q", line)
		}
	}
	if strings.Contains(env, "ZIMAM_AGENT") {
		t.Errorf(".env sets ZIMAM_AGENT, so the panel wouldn't use the socket:\n%s", env)
	}
	for _, c := range w.sys.commands() {
		if strings.Contains(argv(c)+strings.Join(c.Env, " "), key[1]) {
			t.Errorf("APP_KEY is in the command %s", argv(c))
		}
	}
	for _, e := range w.eventsSince(0) {
		if strings.Contains(e, key[1]) {
			t.Errorf("APP_KEY is in the call %s", e)
		}
	}
	if strings.Contains(mustRead(t, w.p.AuditLog()), key[1]) || strings.Contains(w.logs.String(), key[1]) {
		t.Error("APP_KEY is in the audit log or the output")
	}
}

func TestExistingEnvKept(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	w.help.Mkdir(ctx, w.b.Zimam, "panel/shared", 0o700)
	w.help.Write(ctx, w.b.Zimam, "panel/shared/.env", 0o600, strings.NewReader("APP_KEY=base64:kept\n"))
	w.help.Write(ctx, w.b.Zimam, "panel/shared/database.sqlite", 0o600, strings.NewReader("SQLite format 3\x00"))
	w.help.writes = nil
	w.mustRun(opts())
	if _, ok := w.help.writes["panel/shared/.env"]; ok {
		t.Error("bootstrap replaced an existing .env")
	}
	if _, ok := w.help.writes["panel/shared/database.sqlite"]; ok {
		t.Error("bootstrap replaced an existing database")
	}
}

func TestRootNeverTouchesHomeZimam(t *testing.T) {
	w := newWorld(t)
	w.mustRun(opts())
	if _, err := os.Lstat(filepath.Join(w.p.Root, "home")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("root made something under /home (lstat: %v)", err)
	}
	for _, c := range w.sys.commands() {
		if strings.HasPrefix(c.Dir, "/home") || strings.HasPrefix(c.Dir, w.p.PanelHome()) || c.Dir == "" && strings.Contains(argv(c), "/home/") {
			t.Errorf("%s runs in %q", argv(c), c.Dir)
		}
		if strings.Contains(argv(c), "/home/") && (c.Path != "/usr/sbin/runuser" || !slices.Equal(c.Args[:3], []string{"-u", "zimam", "--"})) {
			t.Errorf("%s names a path under /home but doesn't run as zimam", argv(c))
		}
	}
	for _, a := range w.help.accounts {
		if a != "zimam" {
			t.Errorf("a helper call ran as %q", a)
		}
	}
}

func TestDirectoriesModes(t *testing.T) {
	w := newWorld(t)
	w.mustRun(opts())
	p, ids := w.p, w.b.IDs
	for _, d := range []struct {
		path     string
		mode     fs.FileMode
		uid, gid int
	}{
		{p.ConfigDir(), 0o755, ids.RootUID, ids.RootGID},
		{p.LogrotateDir(), 0o755, ids.RootUID, ids.RootGID},
		{p.RunDir(), 0o755, ids.RootUID, ids.RootGID},
		{p.LocksDir(), 0o700, ids.RootUID, ids.RootGID},
		{p.StateDir(), 0o700, ids.RootUID, ids.RootGID},
		{p.JobsDir(), 0o700, ids.RootUID, ids.RootGID},
		{p.SitesStateDir(), 0o700, ids.RootUID, ids.RootGID},
		{p.DownloadsDir(), 0o700, ids.RootUID, ids.RootGID},
		{p.CertsDir(), 0o700, ids.RootUID, ids.RootGID},
		{p.LogDir(), 0o750, ids.RootUID, ids.AgentGID},
		{p.JobLogsDir(), 0o750, ids.RootUID, ids.AgentGID},
		{p.SitesLogDir(), 0o750, ids.RootUID, ids.RootGID},
		{p.ACMEWebroot(), 0o755, ids.RootUID, ids.RootGID},
		{p.NginxZimamDir(), 0o755, ids.RootUID, ids.RootGID},
	} {
		fi, err := os.Lstat(d.path)
		if err != nil {
			t.Errorf("%v", err)
			continue
		}
		st := fi.Sys().(*syscall.Stat_t)
		if !fi.IsDir() || fi.Mode().Perm() != d.mode || int(st.Uid) != d.uid || int(st.Gid) != d.gid {
			t.Errorf("%s: %v %d:%d, want a directory %v %d:%d", d.path, fi.Mode(), st.Uid, st.Gid, d.mode, d.uid, d.gid)
		}
	}
	if fi, err := os.Lstat(p.AuditLog()); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("audit.log: %v, %v; want 0600", fi.Mode(), err)
	}
	for name, mode := range map[string]fs.FileMode{
		"panel": 0o750, "panel/releases": 0o750, "panel/shared": 0o700, "panel/shared/storage/tmp": 0o750, "panel/shared/storage/logs": 0o750,
	} {
		if n := w.help.node(name); n == nil || !n.dir || n.mode != mode {
			t.Errorf("helper made %s as %+v, want a directory %v", name, n, mode)
		}
	}
	for name, mode := range map[string]fs.FileMode{"panel/shared/.env": 0o600, "panel/shared/database.sqlite": 0o600} {
		if n := w.help.node(name); n == nil || n.dir || n.mode != mode {
			t.Errorf("helper wrote %s as %+v, want a file %v", name, n, mode)
		}
	}
}

func TestRefusesSymlinkedDirectory(t *testing.T) {
	w := newWorld(t)
	elsewhere := t.TempDir()
	if err := os.Symlink(elsewhere, w.p.ACMEWebroot()); err != nil {
		t.Fatal(err)
	}
	_, err := w.b.Run(context.Background(), opts())
	var se *StepError
	if !errors.As(err, &se) || se.Step != "directories" || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("got %v, want a directories failure naming the symlink", err)
	}
	if fi, _ := os.Lstat(w.p.ACMEWebroot()); fi.Mode()&fs.ModeSymlink == 0 {
		t.Fatal("the symlink was replaced")
	}
}

func TestRefusesWrongOwner(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "zimam")
	mustMkdir(t, dir)
	err := ensureDir(dirSpec{path: dir, mode: 0o755, uid: os.Getuid() + 1, gid: os.Getgid()})
	if err == nil || !strings.Contains(err.Error(), "owner") {
		t.Fatalf("got %v, want a wrong-owner error", err)
	}
}

func TestDryRunBeforeRealCertificate(t *testing.T) {
	w := newWorld(t)
	w.mustRun(opts())
	var certbot []string
	for _, c := range w.sys.commands() {
		if c.Path == "/usr/bin/certbot" {
			certbot = append(certbot, argv(c))
		}
	}
	if len(certbot) != 2 || !strings.HasSuffix(certbot[0], " --dry-run") || strings.Contains(certbot[1], "--dry-run") {
		t.Fatalf("certbot runs %q, want a dry run, then the real request", certbot)
	}
	vhost := mustRead(t, w.p.NginxPanelAvailable())
	if !strings.Contains(vhost, "listen 443 ssl;") || !strings.Contains(vhost, "return 301 https://") {
		t.Fatalf("the panel vhost isn't HTTPS with a redirect:\n%s", vhost)
	}
	rec := mustRead(t, w.p.CertRecord("panel"))
	if !strings.Contains(rec, `"name": "panel"`) {
		t.Fatalf("panel certificate record: %s", rec)
	}
}

func TestDryRunFailureHint(t *testing.T) {
	w := newWorld(t)
	w.sys.fail = func(c sysexec.Cmd) error {
		if c.Path == "/usr/bin/certbot" && slices.Contains(c.Args, "--dry-run") {
			return &sysexec.ExitError{Path: c.Path, Code: 1, Stderr: []byte("Connection refused")}
		}
		return nil
	}
	_, err := w.b.Run(context.Background(), opts())
	var se *StepError
	if !errors.As(err, &se) || !strings.Contains(se.Hint, "DNS for panel.example.com doesn't point at this server") {
		t.Fatalf("got %v (hint %q), want the DNS hint", err, se.Hint)
	}
	for _, c := range w.sys.commands() {
		if c.Path == "/usr/bin/certbot" && !slices.Contains(c.Args, "--dry-run") {
			t.Fatal("certbot asked for a real certificate after the dry run failed")
		}
	}
	if vhost := mustRead(t, w.p.NginxPanelAvailable()); strings.Contains(vhost, "443") {
		t.Fatal("the panel vhost switched to HTTPS without a certificate")
	}
}

func TestTimersEnabled(t *testing.T) {
	w := newWorld(t)
	w.mustRun(opts())
	var enabled []string
	for _, c := range w.sys.commands() {
		if c.Path == "/usr/bin/systemctl" && len(c.Args) == 3 && c.Args[0] == "enable" && c.Args[1] == "--now" {
			enabled = append(enabled, c.Args[2])
		}
	}
	for _, u := range []string{"zimam-panel-queue.service", "zimam-panel-scheduler.timer", "zimam-maintenance.timer"} {
		if !slices.Contains(enabled, u) {
			t.Errorf("%s isn't enabled (enable --now ran for %q)", u, enabled)
		}
		if _, err := os.Stat(w.p.SystemdUnit(u)); err != nil {
			t.Error(err)
		}
	}
	if unit := mustRead(t, w.p.SystemdUnit("zimam-panel-queue.service")); !strings.Contains(unit, "/usr/bin/php8.5 artisan queue:work") {
		t.Errorf("the queue worker doesn't run on the panel's PHP:\n%s", unit)
	}
}

// A repair run starts a queue worker that is enabled but dead (M1 review,
// M12): running the installer again has to bring job tracking back.
func TestRerunStartsADeadQueueWorker(t *testing.T) {
	w := newWorld(t)
	w.mustRun(opts())
	before := len(w.sys.commands())
	w.sys.fail = func(c sysexec.Cmd) error {
		if c.Path == "/usr/bin/systemctl" && slices.Equal(c.Args, []string{"is-active", "--quiet", "zimam-panel-queue.service"}) {
			return &sysexec.ExitError{Path: c.Path, Code: 3}
		}
		return nil
	}
	w.mustRun(opts())
	var started bool
	for _, c := range w.sys.commands()[before:] {
		started = started || argv(c) == "/usr/bin/systemctl enable --now zimam-panel-queue.service"
	}
	if !started {
		t.Error("the re-run didn't start the dead queue worker")
	}
}

func TestDefaultLogPolicyApplied(t *testing.T) {
	w := newWorld(t)
	w.mustRun(opts())
	cfg, err := config.Load(w.p)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.AuditRetentionDays != 365 {
		t.Errorf("agent.json: audit retention %d days, want 365 (D24)", cfg.AuditRetentionDays)
	}
	if cfg.LogRetentionDays != 14 || int64(cfg.SiteLogCapBytes) != 5*contract.GiB {
		t.Fatalf("agent.json: %d days, %d bytes; want 14 and 5 GiB", cfg.LogRetentionDays, cfg.SiteLogCapBytes)
	}
	if own := mustRead(t, w.p.OwnLogrotateConf()); !strings.Contains(own, "maxage 14\n") {
		t.Fatalf("zimam-own.conf doesn't keep 14 days:\n%s", own)
	}
	ran := map[string]bool{}
	for _, c := range w.sys.commands() {
		ran[argv(c)] = true
	}
	if !ran["/usr/bin/systemctl restart systemd-journald"] {
		t.Fatal("journald wasn't restarted")
	}
}

func TestEachAppliedStepAudited(t *testing.T) {
	w := newWorld(t)
	w.mustRun(opts())
	entries := w.auditEntries()
	if len(entries) != len(designOrder) {
		t.Fatalf("%d audit entries, want one per step (%d)", len(entries), len(designOrder))
	}
	for i, e := range entries {
		if e["actor"] != "cli:root" || e["command"] != "bootstrap" || e["peer_uid"] != nil || e["outcome"] != "ok" {
			t.Errorf("entry %d: %v", i, e)
		}
		if params := e["params"].(map[string]any); params["step"] != designOrder[i] {
			t.Errorf("entry %d is for %v, want %s", i, params["step"], designOrder[i])
		}
	}
}

func TestPrintsSetupLink(t *testing.T) {
	w := newWorld(t)
	if link := w.mustRun(opts()); link != testLink {
		t.Fatalf("Run returned %q, want only the link, %q", link, testLink)
	}
	last := w.sys.commands()[len(w.sys.commands())-1]
	want := []string{"-u", "zimam", "--", "/usr/bin/php8.5", w.p.PanelCurrent() + "/artisan", "zimam:setup-link"}
	if last.Path != "/usr/sbin/runuser" || !slices.Equal(last.Args, want) || !slices.Contains(last.Env, "HOME=/home/zimam") {
		t.Fatalf("the last command is %s (env %q), want runuser %q with HOME=/home/zimam", argv(last), last.Env, want)
	}
	token := testLink[strings.LastIndex(testLink, "/")+1:]
	if strings.Contains(mustRead(t, w.p.AuditLog()), token) || strings.Contains(w.logs.String(), token) {
		t.Fatal("the setup link's token is in the audit log or the output")
	}
}

func TestMigrateBeforeCurrentMoves(t *testing.T) {
	w := newWorld(t)
	w.mustRun(opts())
	events := w.eventsSince(0)
	migrate := slices.IndexFunc(events, func(e string) bool { return strings.Contains(e, "artisan migrate --force") })
	current := slices.IndexFunc(events, func(e string) bool { return strings.HasPrefix(e, "helper symlink-swap panel/current ") })
	if migrate < 0 || current < 0 || migrate > current {
		t.Fatalf("migrate at %d, current moved at %d; want migrate first", migrate, current)
	}
	if !strings.Contains(events[migrate], " "+w.p.PanelReleasesDir()+"/"+testVersion+"/artisan ") {
		t.Fatalf("migrate doesn't run the new release's artisan: %s", events[migrate])
	}
	for link, target := range map[string]string{
		"panel/current":                                 "releases/" + testVersion,
		"panel/releases/0.1.0/.env":                     "../../shared/.env",
		"panel/releases/0.1.0/storage":                  "../../shared/storage",
		"panel/releases/0.1.0/database/database.sqlite": "../../../shared/database.sqlite",
	} {
		if n := w.help.node(link); n == nil || n.target != target {
			t.Errorf("%s: %+v, want a link to %s", link, n, target)
		}
	}
	if lim := w.help.limits[0]; lim.MaxEntries <= 18189 || lim.MaxBytes <= 67226993 {
		t.Errorf("extract limits %+v leave no room above the 0.0.0-dev tarball", lim)
	}
	if _, err := os.Stat(filepath.Join(w.p.DownloadsDir(), "zimam-panel-"+testVersion+".tar.gz")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the downloaded tarball was kept: %v", err)
	}
}

func TestPartialReleaseExtractedAgain(t *testing.T) {
	w := newWorld(t)
	w.help.failExtract = errors.New("disk full")
	if _, err := w.b.Run(context.Background(), opts()); err == nil {
		t.Fatal("bootstrap succeeded with a failing extract")
	}
	w.help.failExtract = nil
	ctx := context.Background()
	w.help.Mkdir(ctx, w.b.Zimam, "panel/releases/"+testVersion+"/public", 0o750) // what the failed extract left
	w.mustRun(opts())
	if n := w.help.node("panel/releases/" + testVersion + "/artisan"); n == nil {
		t.Fatal("the release wasn't extracted again")
	}
}

func TestTamperedTarballRefused(t *testing.T) {
	w := newWorld(t)
	tarball := w.http.files[testBaseURL+"/download/v"+testVersion+"/zimam-panel-"+testVersion+".tar.gz"]
	tampered := append(slices.Clone(tarball), 0)
	w.http.files[testBaseURL+"/download/v"+testVersion+"/zimam-panel-"+testVersion+".tar.gz"] = tampered
	_, err := w.b.Run(context.Background(), opts())
	var se *StepError
	if !errors.As(err, &se) || se.Step != "panel-release" || !strings.Contains(err.Error(), "does not match the signed manifest") {
		t.Fatalf("got %v, want panel-release to refuse the tarball", err)
	}
	if len(w.help.limits) != 0 {
		t.Fatal("a tarball that doesn't match the manifest was extracted")
	}
}

func TestManifestSignatureChecked(t *testing.T) {
	w := newWorld(t)
	_, other, _ := ed25519.GenerateKey(rand.Reader)
	w.publish(other, panelTarball(t), nil)
	_, err := w.b.Run(context.Background(), opts())
	if !errors.Is(err, release.ErrBadSignature) {
		t.Fatalf("got %v, want the bad-signature error", err)
	}
	if _, err := os.Stat(w.p.AgentConfig()); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("agent.json was written from an unverified manifest")
	}
}

func TestManifestVersionMustMatch(t *testing.T) {
	w := newWorld(t)
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	w.b.PublicKey = priv.Public().(ed25519.PublicKey)
	w.publish(priv, panelTarball(t), func(m *release.Manifest) { m.Version = "0.0.9"; m.MinAgentVersion = "0.0.9" })
	if _, err := w.b.Run(context.Background(), opts()); err == nil || !strings.Contains(err.Error(), "0.0.9") {
		t.Fatalf("got %v, want a refusal of the 0.0.9 manifest served for 0.1.0", err)
	}
}

// A re-run never moves the panel to an older release (M1 review, M5): the
// older panel's code would run against the newer schema.
func TestRerunRefusesAnOlderRelease(t *testing.T) {
	w := newWorld(t)
	w.mustRun(opts())
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	w.b.PublicKey = priv.Public().(ed25519.PublicKey)
	w.publish(priv, panelTarball(t), func(m *release.Manifest) { m.Version = "0.0.9"; m.MinAgentVersion = "0.0.9" })
	o := opts()
	o.Version = "0.0.9"
	_, err := w.b.Run(context.Background(), o)
	var se *StepError
	if !errors.As(err, &se) || se.Step != "panel-release" || !strings.Contains(err.Error(), "older") {
		t.Fatalf("got %v, want panel-release to refuse the older release", err)
	}
	if fi, _ := w.help.Stat(context.Background(), w.b.Zimam, "panel/current"); fi.Target != "releases/"+testVersion {
		t.Errorf("current is %q, want releases/%s", fi.Target, testVersion)
	}
}

func TestNginxDefaultSiteRestoredOnFailure(t *testing.T) {
	w := newWorld(t)
	w.sys.fail = func(c sysexec.Cmd) error {
		if c.Path == "/usr/sbin/nginx" {
			return &sysexec.ExitError{Path: c.Path, Code: 1, Stderr: []byte("nginx: [emerg] bad")}
		}
		return nil
	}
	_, err := w.b.Run(context.Background(), opts())
	var se *StepError
	if !errors.As(err, &se) || se.Step != "nginx" {
		t.Fatalf("got %v, want a failure at nginx", err)
	}
	if target, err := os.Readlink(w.p.NginxDistDefault()); err != nil || target != "/etc/nginx/sites-available/default" {
		t.Fatalf("sites-enabled/default: %q, %v; want it back", target, err)
	}
	if _, err := os.Lstat(w.p.NginxDefaultEnabled()); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("zimam-default.conf stayed enabled: %v", err)
	}
}

func TestDomainChangeRefused(t *testing.T) {
	w := newWorld(t)
	w.mustRun(opts())
	o := opts()
	o.PanelDomain = "other.example.com"
	_, err := w.b.Run(context.Background(), o)
	var se *StepError
	if !errors.As(err, &se) || se.Step != "agent-config" || !strings.Contains(err.Error(), testDomain) {
		t.Fatalf("got %v, want agent-config to refuse a new panel domain", err)
	}
}

func TestRerunKeepsUnflaggedSettings(t *testing.T) {
	w := newWorld(t)
	o := opts()
	o.PHP = "8.3"
	o.ACMEServer = "https://pebble.example.com:14000/dir"
	o.ACMECABundle = "/root/pebble-ca.pem"
	w.mustRun(o)
	w.mustRun(opts())
	cfg, err := config.Load(w.p)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DefaultPHP != "8.3" || cfg.ACMEDirectory != o.ACMEServer || cfg.ACMECABundle != o.ACMECABundle {
		t.Fatalf("a re-run without flags changed agent.json: %+v", cfg)
	}
}

func TestRerunAppliesChangedFlags(t *testing.T) {
	w := newWorld(t)
	w.mustRun(opts())
	entries := len(w.auditEntries())
	o := opts()
	o.PHP = "8.3"
	w.mustRun(o)
	cfg, err := config.Load(w.p)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DefaultPHP != "8.3" {
		t.Fatalf("default_php %s after a re-run with --php 8.3", cfg.DefaultPHP)
	}
	if got := w.auditedSteps(entries); !slices.Equal(got, []string{"agent-config", "php", "setup-link"}) {
		t.Fatalf("the re-run applied %q, want agent-config, php and setup-link", got)
	}
}
