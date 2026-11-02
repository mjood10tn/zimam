package bootstrap

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"syscall"
	"time"

	"zimam/agent/contract"
	"zimam/agent/internal/acme"
	"zimam/agent/internal/config"
	"zimam/agent/internal/configtx"
	"zimam/agent/internal/logpolicy"
	"zimam/agent/internal/nginx"
	"zimam/agent/internal/php"
	"zimam/agent/internal/sysexec"
	"zimam/agent/internal/systemd"
	"zimam/agent/internal/templates"
)

const systemctl = "/usr/bin/systemctl"

// steps are §4's, in its order. Each Done only reads; a Done that can't
// tell returns false, and Apply then does the work or says why it can't.
func (r *run) steps() []Step {
	return []Step{
		{"agent-config", r.configDone, r.configApply},
		{"directories", r.dirsDone, r.dirsApply},
		{"sury-repository", r.repoDone, r.repoApply},
		{"php", r.phpDone, r.phpApply},
		{"nginx", r.nginxDone, r.nginxApply},
		{"panel-release", r.releaseDone, r.releaseApply},
		{"panel-site", r.siteDone, r.siteApply},
		{"panel-certificate", r.certDone, r.certApply},
		{"units", r.unitsDone, r.unitsApply},
		{"log-policy", r.logsDone, r.logsApply},
		// The link is the run's output, so this step always runs. A new
		// link only replaces unused ones; an admin's password and 2FA
		// change only when someone opens it.
		{"setup-link", func(context.Context) (bool, error) { return false, nil }, r.setupLinkApply},
	}
}

// deps is the run's Deps with the config this run settled on.
func (r *run) deps() sysexec.Deps {
	d := r.Deps
	d.Config = r.cfg
	return d
}

// Step 1: /etc/zimam/agent.json (§5.9).

// configDone settles the config this run wants: the stored one with the
// flags applied, or a new one with the defaults. It is done when the file
// already says that.
func (r *run) configDone(ctx context.Context) (bool, error) {
	stored, err := config.Load(r.Deps.Paths)
	saved := err == nil
	switch {
	case saved:
		if stored.PanelDomain != r.o.PanelDomain {
			return false, fmt.Errorf("this server's panel domain is %s; changing it to %s isn't supported", stored.PanelDomain, r.o.PanelDomain)
		}
		r.cfg = stored
	case errors.Is(err, fs.ErrNotExist):
		m, err := r.release(ctx)
		if err != nil {
			return false, err
		}
		panelPHP, err := panelPHP(m.PanelPHPMin, r.Deps.Now())
		if err != nil {
			return false, err
		}
		r.cfg = config.New(r.o.PanelDomain, newestSupported(r.Deps.Now()), panelPHP)
	default:
		return false, err // never replace a file that can't be read
	}
	want := r.cfg
	if r.o.PHP != "" {
		if err := supported(r.o.PHP, r.Deps.Now()); err != nil {
			return false, err
		}
		want.DefaultPHP = r.o.PHP
	}
	if r.o.Email != "" {
		want.ACMEEmail = r.o.Email
	}
	if r.o.ACMEServer != "" {
		want.ACMEDirectory = r.o.ACMEServer
	}
	if r.o.ACMECABundle != "" {
		want.ACMECABundle = r.o.ACMECABundle
	}
	if err := want.Validate(); err != nil {
		return false, err
	}
	done := saved && want == r.cfg
	r.cfg = want
	return done, nil
}

func (r *run) configApply(ctx context.Context) error {
	return config.Save(r.Deps.Paths, r.cfg)
}

// panelPHP is the version the panel runs on (§5.9): the newest allowlisted
// version that meets the release's minimum and is supported today.
func panelPHP(min string, now time.Time) (contract.PHPVersion, error) {
	for _, rel := range slices.Backward(contract.PHPAllowlist) {
		if compareMinor(string(rel.Version), min) >= 0 && rel.SupportedOn(now) {
			return rel.Version, nil
		}
	}
	return "", fmt.Errorf("no supported PHP version meets the panel's minimum, %s", min)
}

// newestSupported is --php's default: the newest allowlisted version that
// is supported today.
func newestSupported(now time.Time) contract.PHPVersion {
	for _, rel := range slices.Backward(contract.PHPAllowlist) {
		if rel.SupportedOn(now) {
			return rel.Version
		}
	}
	return contract.PHPAllowlist[len(contract.PHPAllowlist)-1].Version
}

func supported(v contract.PHPVersion, now time.Time) error {
	rel, ok := v.Release()
	if !ok {
		return fmt.Errorf("PHP %s isn't on this agent's allowlist", v)
	}
	if !rel.SupportedOn(now) {
		return fmt.Errorf("PHP %s's security support ended on %s", v, rel.SecurityEnd)
	}
	return nil
}

// compareMinor compares two <major>.<minor> versions numerically.
func compareMinor(a, b string) int {
	var am, an, bm, bn int
	fmt.Sscanf(a, "%d.%d", &am, &an)
	fmt.Sscanf(b, "%d.%d", &bm, &bn)
	if c := am - bm; c != 0 {
		return c
	}
	return an - bn
}

// Step 2: directories (§5.9).

// dirSpec is a directory root owns, with its mode and owner.
type dirSpec struct {
	path     string
	mode     fs.FileMode
	uid, gid int
}

func (r *run) rootDirs() []dirSpec {
	p, ids := r.Deps.Paths, r.IDs
	root := func(path string, mode fs.FileMode) dirSpec { return dirSpec{path, mode, ids.RootUID, ids.RootGID} }
	agent := func(path string) dirSpec { return dirSpec{path, 0o750, ids.RootUID, ids.AgentGID} }
	return []dirSpec{
		root(p.ConfigDir(), 0o755),
		root(p.LogrotateDir(), 0o755),
		root(p.RunDir(), 0o755),
		root(p.LocksDir(), 0o700),
		root(p.StateDir(), 0o700),
		root(p.JobsDir(), 0o700),
		root(p.SitesStateDir(), 0o700),
		root(p.DownloadsDir(), 0o700),
		root(p.CertsDir(), 0o700),
		agent(p.LogDir()),
		agent(p.JobLogsDir()),
		root(p.SitesLogDir(), 0o750),
		root(p.ACMEWebroot(), 0o755),
		root(p.NginxZimamDir(), 0o755),
	}
}

// auditDirs are what the audit writer needs before the first entry.
func (r *run) auditDirs() []dirSpec {
	p := r.Deps.Paths
	return slices.DeleteFunc(r.rootDirs(), func(d dirSpec) bool {
		return d.path != p.RunDir() && d.path != p.LocksDir() && d.path != p.LogDir()
	})
}

func (r *run) dirsDone(ctx context.Context) (bool, error) {
	for _, d := range r.rootDirs() {
		fi, err := os.Lstat(d.path)
		if err != nil || !fi.IsDir() || fi.Mode().Perm() != d.mode {
			return false, nil
		}
		if st := fi.Sys().(*syscall.Stat_t); int(st.Uid) != d.uid || int(st.Gid) != d.gid {
			return false, nil
		}
	}
	return true, nil
}

func (r *run) dirsApply(ctx context.Context) error { return ensureDirs(r.rootDirs()) }

func ensureDirs(dirs []dirSpec) error {
	for _, d := range dirs {
		if err := ensureDir(d); err != nil {
			return err
		}
	}
	return nil
}

// ensureDir makes d, or brings an existing one to its group and mode. It
// refuses a symlink, something that isn't a directory, and a directory
// another user owns: Zimam doesn't take over what it didn't make.
func ensureDir(d dirSpec) error {
	fi, err := os.Lstat(d.path)
	if errors.Is(err, fs.ErrNotExist) {
		if err := os.MkdirAll(filepath.Dir(d.path), 0o755); err != nil {
			return err
		}
		if err := os.Mkdir(d.path, d.mode); err != nil {
			return err
		}
		fi, err = os.Lstat(d.path)
	}
	if err != nil {
		return err
	}
	switch {
	case fi.Mode()&fs.ModeSymlink != 0:
		return fmt.Errorf("%s is a symlink, not a directory", d.path)
	case !fi.IsDir():
		return fmt.Errorf("%s exists and isn't a directory", d.path)
	}
	st := fi.Sys().(*syscall.Stat_t)
	if int(st.Uid) != d.uid {
		return fmt.Errorf("%s has the owner uid %d, not %d", d.path, st.Uid, d.uid)
	}
	if int(st.Gid) != d.gid {
		if err := os.Lchown(d.path, d.uid, d.gid); err != nil {
			return err
		}
	}
	// chown can clear mode bits, and Mkdir's mode went through the umask.
	return os.Chmod(d.path, d.mode)
}

// Step 3: the sury repository (§8.2).

func (r *run) repoDone(ctx context.Context) (bool, error) {
	repo, err := url.Parse(r.cfg.PHPRepoURL)
	if err != nil {
		return false, err
	}
	data := templates.SuryRepo{URL: r.cfg.PHPRepoURL, Host: repo.Hostname()}
	return exists(r.Deps.Paths.SuryKeyring()) &&
		rendered(r.Deps.Paths.SurySources(), "apt/zimam-sury.sources", data) &&
		rendered(r.Deps.Paths.SuryPin(), "apt/zimam-sury.pref", data), nil
}

func (r *run) repoApply(ctx context.Context) error { return php.EnsureRepo(ctx, r.deps()) }

// Step 4: PHP (§4 step 4): the panel's version, the default if it differs,
// and the default for the php command.

func (r *run) phpVersions() []contract.PHPVersion {
	vs := []contract.PHPVersion{r.cfg.PanelPHP}
	if r.cfg.DefaultPHP != r.cfg.PanelPHP {
		vs = append(vs, r.cfg.DefaultPHP)
	}
	return vs
}

func (r *run) phpDone(ctx context.Context) (bool, error) {
	p := r.Deps.Paths
	for _, v := range r.phpVersions() {
		if !exists(p.PHPFPMBinary(v)) || exists(p.PHPWwwPool(v)) ||
			!rendered(p.PHPIni(v), "php/99-zimam.ini", nil) || !rendered(p.PHPGlobalConf(v), "php/00-zimam-global.conf", nil) {
			return false, nil
		}
	}
	target, err := os.Readlink(p.Alternative("php"))
	return err == nil && target == "/usr/bin/php"+string(r.cfg.DefaultPHP), nil
}

func (r *run) phpApply(ctx context.Context) error {
	d := r.deps()
	for _, v := range r.phpVersions() {
		if err := php.Install(ctx, d, v); err != nil {
			return err
		}
	}
	return php.SetDefault(ctx, d, r.cfg.DefaultPHP)
}

// Step 5: Nginx (§9.3): the default server and the TLS snippet, without
// the package's default site.

func (r *run) nginxDone(ctx context.Context) (bool, error) {
	p := r.Deps.Paths
	if _, err := os.Lstat(p.NginxDistDefault()); !errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	target, err := os.Readlink(p.NginxDefaultEnabled())
	return err == nil && target == p.NginxDefaultAvailable() &&
		rendered(p.NginxDefaultAvailable(), "nginx/default.conf", nil) &&
		rendered(p.NginxTLSConf(), "nginx/tls.conf", nil), nil
}

func (r *run) nginxApply(ctx context.Context) error {
	p := r.Deps.Paths
	def, err := templates.Render("nginx/default.conf", nil)
	if err != nil {
		return err
	}
	tls, err := templates.Render("nginx/tls.conf", nil)
	if err != nil {
		return err
	}
	// configtx keeps a replaced file beside it until the test passes, and
	// Nginx would load a kept sites-enabled/default together with the new
	// default server. So the link goes first, and comes back on failure.
	distTarget, err := os.Readlink(p.NginxDistDefault())
	removed := false
	switch {
	case err == nil:
		if err := os.Remove(p.NginxDistDefault()); err != nil {
			return err
		}
		removed = true
	case !errors.Is(err, fs.ErrNotExist):
		return fmt.Errorf("%s isn't a symlink; move it away so Zimam's default server can answer: %w", p.NginxDistDefault(), err)
	}
	err = nginx.ApplySite(ctx, r.deps(), []configtx.Change{
		configtx.RootFile(p.NginxTLSConf(), tls, 0o644),
		configtx.RootFile(p.NginxDefaultAvailable(), def, 0o644),
	}, nginx.Link{Path: p.NginxDefaultEnabled(), Target: p.NginxDefaultAvailable()})
	if err != nil && removed {
		if rerr := os.Symlink(distTarget, p.NginxDistDefault()); rerr != nil {
			err = fmt.Errorf("%w; restoring %s: %v", err, p.NginxDistDefault(), rerr)
		}
	}
	return err
}

// Step 7: the panel's PHP pool and vhost (§7.1, §9.3).

// panelVhost renders the panel's vhost: HTTPS once its certificate exists.
func (r *run) panelVhost(https bool) ([]byte, error) {
	return templates.Render("nginx/panel.conf", templates.PanelVhost{Domain: r.cfg.PanelDomain, HTTPS: https})
}

func (r *run) certExists() bool { return exists(r.Deps.Paths.LetsEncryptCert("zimam-panel")) }

func (r *run) siteDone(ctx context.Context) (bool, error) {
	p := r.Deps.Paths
	http, err := r.panelVhost(false)
	if err != nil {
		return false, err
	}
	https, err := r.panelVhost(true)
	if err != nil {
		return false, err
	}
	vhost := fileIs(p.NginxPanelAvailable(), http) || fileIs(p.NginxPanelAvailable(), https)
	target, err := os.Readlink(p.NginxPanelEnabled())
	if !vhost || err != nil || target != p.NginxPanelAvailable() || !rendered(p.PanelPool(r.cfg.PanelPHP), "php/panel-pool.conf", nil) {
		return false, nil
	}
	return r.active(ctx, "php"+string(r.cfg.PanelPHP)+"-fpm"), nil
}

func (r *run) siteApply(ctx context.Context) error {
	d, p, v := r.deps(), r.Deps.Paths, r.cfg.PanelPHP
	pool, err := templates.Render("php/panel-pool.conf", nil)
	if err != nil {
		return err
	}
	d.Logf("Writing the panel's PHP %s pool, then running php-fpm%s -t", v, v)
	err = configtx.WithReloadLock(ctx, p, func() error {
		return configtx.Apply(ctx, []configtx.Change{configtx.RootFile(p.PanelPool(v), pool, 0o644)}, func(ctx context.Context) error {
			return configtx.RunTest(ctx, d.Run, sysexec.Cmd{Path: "/usr/sbin/php-fpm" + string(v), Args: []string{"-t"}})
		})
	})
	if err != nil {
		return err
	}
	if err := php.ApplyFPM(ctx, d, v); err != nil {
		return err
	}
	vhost, err := r.panelVhost(r.certExists())
	if err != nil {
		return err
	}
	return nginx.ApplySite(ctx, d, []configtx.Change{configtx.RootFile(p.NginxPanelAvailable(), vhost, 0o644)},
		nginx.Link{Path: p.NginxPanelEnabled(), Target: p.NginxPanelAvailable()})
}

// Step 8: the panel's certificate (§10), then the vhost with HTTPS and the
// redirect.

func (r *run) certDone(ctx context.Context) (bool, error) {
	https, err := r.panelVhost(true)
	if err != nil {
		return false, err
	}
	return r.certExists() && exists(r.Deps.Paths.CertRecord("panel")) && fileIs(r.Deps.Paths.NginxPanelAvailable(), https), nil
}

func (r *run) certApply(ctx context.Context) error {
	d, p := r.deps(), r.Deps.Paths
	cert := p.LetsEncryptCert("zimam-panel")
	if !r.certExists() {
		req := acme.Request{CertName: "zimam-panel", Domain: r.cfg.PanelDomain, DryRun: true}
		if _, err := acme.Issue(ctx, d, req); err != nil {
			return err
		}
		req.DryRun = false
		expiry, err := acme.Issue(ctx, d, req)
		if err != nil {
			return err
		}
		if err := acme.WriteRecord(d, "panel", expiry); err != nil {
			return err
		}
	} else if !exists(p.CertRecord("panel")) {
		expiry, err := acme.ReadExpiry(cert)
		if err != nil {
			return err
		}
		if err := acme.WriteRecord(d, "panel", expiry); err != nil {
			return err
		}
	}
	vhost, err := r.panelVhost(true)
	if err != nil {
		return err
	}
	d.Logf("Switching the panel to HTTPS")
	return nginx.ApplySite(ctx, d, []configtx.Change{configtx.RootFile(p.NginxPanelAvailable(), vhost, 0o644)},
		nginx.Link{Path: p.NginxPanelEnabled(), Target: p.NginxPanelAvailable()})
}

// Step 9: the panel's queue worker and scheduler, and maintenance (§7.1).

func (r *run) unitsDone(ctx context.Context) (bool, error) {
	units, err := systemd.ZimamUnits(r.cfg.PanelPHP)
	if err != nil {
		return false, err
	}
	for _, u := range units {
		if !fileIs(r.Deps.Paths.SystemdUnit(u.Name), u.Content) {
			return false, nil
		}
	}
	// Enabled units are the queue worker and the timers, which run all the
	// time: enabled but dead (stopped, or past its start limit) isn't done,
	// and unitsApply's enable --now starts it again.
	for _, u := range units {
		if u.Enable && (!r.systemctlOK(ctx, "is-enabled", "--quiet", u.Name) || !r.systemctlOK(ctx, "is-active", "--quiet", u.Name)) {
			return false, nil
		}
	}
	return true, nil
}

func (r *run) unitsApply(ctx context.Context) error {
	units, err := systemd.ZimamUnits(r.cfg.PanelPHP)
	if err != nil {
		return err
	}
	return systemd.InstallUnits(ctx, r.deps(), units)
}

// Step 10: the default log policy (§11.4), from agent.json.

func (r *run) logsDone(ctx context.Context) (bool, error) {
	p := r.Deps.Paths
	policy := templates.LogPolicy{RetentionDays: r.cfg.LogRetentionDays}
	return rendered(p.LogrotateConf(), "logrotate/zimam.conf", nil) &&
		rendered(p.OwnLogrotateConf(), "logrotate/zimam-own.conf", policy) &&
		rendered(p.JournaldDropIn(), "journald/50-zimam.conf", policy), nil
}

func (r *run) logsApply(ctx context.Context) error {
	return logpolicy.Apply(ctx, r.deps(), logpolicy.Policy{RetentionDays: r.cfg.LogRetentionDays, SiteCapBytes: r.cfg.SiteLogCapBytes}, nil)
}

// Step 11: the setup link.

func (r *run) setupLinkApply(ctx context.Context) error {
	_, link, err := SetupLink(ctx, r.deps(), false)
	r.link = link
	return err
}

// active reports whether a unit runs.
func (r *run) active(ctx context.Context, unit string) bool {
	return r.systemctlOK(ctx, "is-active", "--quiet", unit)
}

func (r *run) systemctlOK(ctx context.Context, args ...string) bool {
	_, err := r.Deps.Run.Run(ctx, sysexec.Cmd{Path: systemctl, Args: args})
	return err == nil
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// fileIs reports whether path is a regular file holding exactly content.
func fileIs(path string, content []byte) bool {
	fi, err := os.Lstat(path)
	if err != nil || !fi.Mode().IsRegular() {
		return false
	}
	b, err := os.ReadFile(path)
	return err == nil && bytes.Equal(b, content)
}

// rendered reports whether path holds the template rendered with data.
func rendered(path, name string, data any) bool {
	content, err := templates.Render(name, data)
	return err == nil && fileIs(path, content)
}
