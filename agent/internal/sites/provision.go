package sites

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/user"
	"slices"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"zimam/agent/contract"
	"zimam/agent/internal/config"
	"zimam/agent/internal/configtx"
	"zimam/agent/internal/helper"
	"zimam/agent/internal/logpolicy"
	"zimam/agent/internal/nginx"
	"zimam/agent/internal/php"
	"zimam/agent/internal/sysexec"
	"zimam/agent/internal/templates"
)

const (
	useradd   = "/usr/sbin/useradd"
	usermod   = "/usr/sbin/usermod"
	systemctl = "/usr/bin/systemctl"
)

// Helper is what provisioning and destroying need of the site-user helper:
// root never opens a path inside a site's home (§5.6). *helper.Client is
// the real one.
type Helper interface {
	Mkdir(ctx context.Context, a helper.Account, name string, mode fs.FileMode) error
	Write(ctx context.Context, a helper.Account, name string, mode fs.FileMode, content io.Reader) error
	Stat(ctx context.Context, a helper.Account, name string) (helper.FileInfo, error)
	SymlinkSwap(ctx context.Context, a helper.Account, link, target string) error
	RemoveTree(ctx context.Context, a helper.Account, name string) error
}

// Lookup finds the account the helper runs as; helper.LookupAccount is the
// real one. An account that doesn't exist yet is a user.UnknownUserError.
type Lookup func(username string) (helper.Account, error)

// Check is site.provision's precheck (§9.2). It answers not_found for a PHP
// version that isn't installed and validation_failed for one past its end
// of support. It answers conflict for the panel's domain, for a domain
// another site has, and for a site that exists with another domain, type
// or PHP version: provisioning again is a retry, never a change. agent.json
// and the state files are read on every call.
func Check(d sysexec.Deps, p contract.SiteProvisionParams) error {
	if p.Type == contract.SitePHP {
		if err := checkPHP(d, p.PHPVersion); err != nil {
			return err
		}
	}
	cfg, err := config.Load(d.Paths)
	if err != nil {
		return err
	}
	if p.Domain == cfg.PanelDomain {
		return conflict("%s is the panel's own domain.", p.Domain)
	}
	all, err := All(d.Paths)
	if err != nil {
		return err
	}
	for _, st := range all {
		switch {
		case st.Name == p.Name && (st.Domain != p.Domain || st.Type != p.Type || st.PHPVersion != p.PHPVersion):
			return conflict("Site %s already exists with other settings. Its domain and PHP version change through their own actions.", p.Name)
		case st.Name != p.Name && st.Domain == p.Domain:
			return conflict("Site %s already uses %s.", st.Name, p.Domain)
		}
	}
	return nil
}

func checkPHP(d sysexec.Deps, v contract.PHPVersion) error {
	if err := checkPHPInstalled(d, v); err != nil {
		return err
	}
	r, _ := v.Release() // the params passed Validate
	now := time.Now()
	if d.Now != nil {
		now = d.Now()
	}
	if !r.SupportedOn(now) {
		return contract.FieldErrors{"php_version": fmt.Sprintf("PHP %s's security support ended on %s, so sites can't move to it", v, r.SecurityEnd)}.Err()
	}
	return nil
}

func conflict(format string, args ...any) error {
	return &contract.AgentError{Code: contract.ErrConflict, Message: fmt.Sprintf(format, args...)}
}

// Provision creates a site (§9.2): its user and home, its files through the
// helper, its log directory, its pool for a PHP site, its security.conf and
// HTTP vhost, its log rules, and last its state file. Each step checks
// before it acts, so a retry continues where the last attempt stopped and
// redoes nothing that is already in place.
func Provision(ctx context.Context, d sysexec.Deps, h Helper, lookup Lookup, p contract.SiteProvisionParams) (contract.SiteProvisionResult, error) {
	if err := p.Validate(); err != nil {
		return contract.SiteProvisionResult{}, err
	}
	sec := DefaultSecurity()
	switch old, err := Load(d.Paths, p.Name); {
	case err == nil:
		sec = old.Security // a retry keeps the site's settings
	case !errors.Is(err, fs.ErrNotExist):
		return contract.SiteProvisionResult{}, err
	}
	st := State{Name: p.Name, Domain: p.Domain, Type: p.Type, PHPVersion: p.PHPVersion, TemplateVersion: templates.Version, Security: sec}
	a, err := ensureUser(ctx, d, lookup, p.Name)
	if err != nil {
		return contract.SiteProvisionResult{}, err
	}
	if err := ensureSiteFiles(ctx, h, a, p); err != nil {
		return contract.SiteProvisionResult{}, err
	}
	if err := ensureLogDir(d, p.Name); err != nil {
		return contract.SiteProvisionResult{}, err
	}
	if p.Type == contract.SitePHP {
		if err := ensurePool(ctx, d, p.Name, p.PHPVersion, sec); err != nil {
			return contract.SiteProvisionResult{}, err
		}
	}
	if err := ensureVhost(ctx, d, st); err != nil {
		return contract.SiteProvisionResult{}, err
	}
	if err := logpolicy.ApplySite(ctx, d, p.Name, d.Config.LogRetentionDays); err != nil {
		return contract.SiteProvisionResult{}, err
	}
	if err := Save(d.Paths, st); err != nil {
		return contract.SiteProvisionResult{}, err
	}
	d.Logf("Site %s is ready at http://%s/", p.Name, p.Domain)
	return contract.SiteProvisionResult{Name: p.Name, TemplateVersion: templates.Version}, nil
}

// ensureUser is step 1: the user s_<name> with its own group, its home
// (root makes it, since nobody else may write /home), and www-data in its
// group, so Nginx can read the site and reach its PHP socket. Nginx's next
// reload starts workers with the new group.
func ensureUser(ctx context.Context, d sysexec.Deps, lookup Lookup, name contract.SiteName) (helper.Account, error) {
	username := "s_" + string(name)
	a, err := lookup(username)
	if errors.As(err, new(user.UnknownUserError)) {
		d.Logf("Creating the user %s", username)
		if _, err := d.Run.Run(ctx, sysexec.Cmd{Path: useradd, Args: []string{"-M", "-d", "/home/" + username, "-s", "/usr/sbin/nologin", "-U", username}}); err != nil {
			return helper.Account{}, err
		}
		a, err = lookup(username)
	}
	if err != nil {
		return helper.Account{}, err
	}
	if err := ensureHome(d.Paths.SiteHome(name), a); err != nil {
		return helper.Account{}, err
	}
	member, err := groupHasMember(d.Paths.EtcGroup(), username, "www-data")
	if err != nil {
		return helper.Account{}, err
	}
	if !member {
		d.Logf("Adding www-data to the group %s", username)
		if _, err := d.Run.Run(ctx, sysexec.Cmd{Path: usermod, Args: []string{"-aG", username, "www-data"}}); err != nil {
			return helper.Account{}, err
		}
	}
	return a, nil
}

// ensureHome makes path a directory a owns, mode 0750. Something else
// there, a symlink above all, is refused: root acts on the directory only
// through a descriptor opened without following links. A directory that
// is already there must be a's, or root's and empty, which is what a crash
// right after the mkdir leaves.
func ensureHome(path string, a helper.Account) error {
	fi, err := os.Lstat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if err := os.Mkdir(path, 0o700); err != nil {
			return err
		}
	case err != nil:
		return err
	case fi.Mode().Type() != fs.ModeDir:
		return fmt.Errorf("%s exists and isn't a directory, so it can't be the site's home", path)
	}
	f, err := os.OpenFile(path, os.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return fmt.Errorf("opening the site's home: %w", err)
	}
	defer f.Close()
	var st unix.Stat_t
	if err := unix.Fstat(int(f.Fd()), &st); err != nil {
		return err
	}
	if st.Uid != a.UID {
		names, err := f.Readdirnames(1)
		if st.Uid != 0 || len(names) > 0 || err != io.EOF {
			return fmt.Errorf("%s already exists and isn't %s's", path, a.Name)
		}
	}
	if st.Uid != a.UID || st.Gid != a.GID {
		if err := f.Chown(int(a.UID), int(a.GID)); err != nil {
			return err
		}
	}
	if fs.FileMode(st.Mode)&fs.ModePerm != 0o750 || st.Mode&(unix.S_ISUID|unix.S_ISGID|unix.S_ISVTX) != 0 {
		return f.Chmod(0o750)
	}
	return nil
}

// groupHasMember reports whether member is listed in group's line of
// /etc/group.
func groupHasMember(path, group, member string) (bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	for line := range strings.Lines(string(data)) {
		fields := strings.Split(strings.TrimRight(line, "\n"), ":")
		if len(fields) == 4 && fields[0] == group {
			return slices.Contains(strings.Split(fields[3], ","), member), nil
		}
	}
	return false, nil
}

// ensureSiteFiles is step 2, as the site user through the helper: the
// placeholder in site/releases/initial/public, site/current pointing at
// that release, logs/ and tmp/ (§9.1). A current link that is already
// there is left alone, since a deploy may have moved it.
func ensureSiteFiles(ctx context.Context, h Helper, a helper.Account, p contract.SiteProvisionParams) error {
	index := "index.html"
	if p.Type == contract.SitePHP {
		index = "index.php"
	}
	const public = "site/releases/initial/public"
	missing := func(name string) (bool, error) {
		_, err := h.Stat(ctx, a, name)
		if errors.Is(err, fs.ErrNotExist) {
			return true, nil
		}
		return false, err
	}
	if m, err := missing(public + "/" + index); err != nil {
		return err
	} else if m {
		body, err := templates.Render("site/"+index, templates.SitePlaceholder{Name: p.Name})
		if err != nil {
			return err
		}
		if err := h.Mkdir(ctx, a, public, 0o750); err != nil {
			return err
		}
		if err := h.Write(ctx, a, public+"/"+index, 0o640, bytes.NewReader(body)); err != nil {
			return err
		}
	}
	if m, err := missing("site/current"); err != nil {
		return err
	} else if m {
		if err := h.SymlinkSwap(ctx, a, "site/current", "releases/initial"); err != nil {
			return err
		}
	}
	for _, dir := range []struct {
		name string
		mode fs.FileMode
	}{{"logs", 0o750}, {"tmp", 0o700}} {
		if m, err := missing(dir.name); err != nil {
			return err
		} else if m {
			if err := h.Mkdir(ctx, a, dir.name, dir.mode); err != nil {
				return err
			}
		}
	}
	return nil
}

// ensureLogDir is step 3: /var/log/zimam/sites/<name>/, root's, 0750.
func ensureLogDir(d sysexec.Deps, name contract.SiteName) error {
	dir := d.Paths.SiteLogDir(name)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	return os.Chmod(dir, 0o750)
}

// ensurePool is step 4: the site's pool on v, tested with php-fpm -t, then
// v's FPM service started or reloaded. It holds v's job lock meanwhile, so
// a php.uninstall of v waits for it or has already finished, and checks
// again that v is installed.
func ensurePool(ctx context.Context, d sysexec.Deps, name contract.SiteName, v contract.PHPVersion, sec Security) error {
	unlock, err := sysexec.Lock(ctx, d.Paths.SubjectLock("php_"+string(v)))
	if err != nil {
		return err
	}
	defer unlock()
	if err := checkPHPInstalled(d, v); err != nil {
		return err
	}
	pool, err := templates.Render("php/site-pool.conf", templates.SitePool{
		Name: name, PHPVersion: v, BlockRiskyFunctions: sec.BlockRiskyFunctions, RestrictToSiteFolder: sec.RestrictToSiteFolder,
	})
	if err != nil {
		return err
	}
	path, sock := d.Paths.PHPPool(v, name), d.Paths.PHPSocket(name, v)
	if fileIs(path, pool) && exists(sock) && unitActive(ctx, d, "php"+string(v)+"-fpm") {
		return nil
	}
	d.Logf("Writing %s's PHP %s pool, then running php-fpm%s -t", name, v, v)
	err = configtx.WithReloadLock(ctx, d.Paths, func() error {
		return configtx.Apply(ctx, []configtx.Change{configtx.RootFile(path, pool, 0o644)}, func(ctx context.Context) error {
			return testPools(ctx, d, v)
		})
	})
	if err != nil {
		return err
	}
	if err := php.ApplyFPM(ctx, d, v); err != nil {
		return err
	}
	return waitForSocket(ctx, sock)
}

// socketWait is how long FPM gets to make a new pool's socket. A reload
// returns before FPM has started again, which takes seconds on a small
// server. A variable, so tests can shorten it.
var socketWait = 30 * time.Second

// waitForSocket waits for FPM to make a pool's socket (§9.5), so Nginx
// never points at a socket that isn't there yet.
func waitForSocket(ctx context.Context, sock string) error {
	ctx, cancel := context.WithTimeout(ctx, socketWait)
	defer cancel()
	for !exists(sock) {
		select {
		case <-ctx.Done():
			return fmt.Errorf("PHP-FPM didn't create %s within %s; its log says why", sock, socketWait)
		case <-time.After(100 * time.Millisecond):
		}
	}
	return nil
}

// testPools runs php-fpm<v> -t while v has a pool. With none, there is
// nothing to test: php-fpm -t refuses a config without a pool, and the
// service is stopped instead (php.ApplyFPM).
func testPools(ctx context.Context, d sysexec.Deps, v contract.PHPVersion) error {
	pools, err := php.HasPools(d, v)
	if err != nil || !pools {
		return err
	}
	return configtx.RunTest(ctx, d.Run, sysexec.Cmd{Path: "/usr/sbin/php-fpm" + string(v), Args: []string{"-t"}})
}

func checkPHPInstalled(d sysexec.Deps, v contract.PHPVersion) error {
	installed, err := php.Installed(d, v)
	if err != nil {
		return err
	}
	if !installed {
		return &contract.AgentError{Code: contract.ErrNotFound, Message: fmt.Sprintf("PHP %s isn't installed.", v)}
	}
	return nil
}

// ensureVhost is step 5: security.conf and the vhost, HTTP only until the
// site's certificate exists, enabled, tested and Nginx reloaded. If Nginx
// rejects them, they are removed again, or the previous ones restored.
// site.php re-renders the vhost through it too.
func ensureVhost(ctx context.Context, d sysexec.Deps, st State) error {
	secConf, err := templates.Render("nginx/security.conf", templates.SiteSecurity{HeadersPreset: st.Security.HeadersPreset})
	if err != nil {
		return err
	}
	https := exists(d.Paths.LetsEncryptCert("zimam-" + string(st.Name)))
	vhost, err := templates.Render("nginx/site.conf", templates.SiteVhost{
		Name: st.Name, Domain: st.Domain, PHPVersion: st.PHPVersion, HTTPS: https, ForceHTTPS: https && st.Security.ForceHTTPS,
	})
	if err != nil {
		return err
	}
	avail, enabled, secPath := d.Paths.NginxSiteAvailable(st.Name), d.Paths.NginxSiteEnabled(st.Name), d.Paths.SiteSecurityConf(st.Name)
	if target, err := os.Readlink(enabled); err == nil && target == avail && fileIs(avail, vhost) && fileIs(secPath, secConf) {
		return nil
	}
	if err := os.MkdirAll(d.Paths.SiteNginxDir(st.Name), 0o755); err != nil {
		return err
	}
	d.Logf("Writing %s's vhost and security settings, then running nginx -t", st.Name)
	return nginx.ApplySite(ctx, d, []configtx.Change{
		configtx.RootFile(secPath, secConf, 0o644),
		configtx.RootFile(avail, vhost, 0o644),
	}, nginx.Link{Path: enabled, Target: avail})
}

// unitActive runs systemctl is-active: exit 0 means active, any other exit
// code that it isn't.
func unitActive(ctx context.Context, d sysexec.Deps, unit string) bool {
	_, err := d.Run.Run(ctx, sysexec.Cmd{Path: systemctl, Args: []string{"is-active", "--quiet", unit}})
	return err == nil
}

func fileIs(path string, content []byte) bool {
	fi, err := os.Lstat(path)
	if err != nil || !fi.Mode().IsRegular() {
		return false
	}
	b, err := os.ReadFile(path)
	return err == nil && bytes.Equal(b, content)
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}
