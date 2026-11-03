package sites

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/netip"
	"os"
	"reflect"
	"strconv"
	"strings"

	"zimam/agent/contract"
	"zimam/agent/internal/configtx"
	"zimam/agent/internal/nginx"
	"zimam/agent/internal/php"
	"zimam/agent/internal/sysexec"
	"zimam/agent/internal/templates"
)

// CheckSecurity is site.security's precheck: not_found for a site that
// doesn't exist, conflict for force_https on a site without a certificate
// (§9.6).
func CheckSecurity(d sysexec.Deps, p contract.SiteSecurityParams) error {
	_, err := Load(d.Paths, p.Name)
	if errors.Is(err, fs.ErrNotExist) {
		return &contract.AgentError{Code: contract.ErrNotFound, Message: fmt.Sprintf("Site %s doesn't exist.", p.Name)}
	}
	if err != nil {
		return err
	}
	if p.ForceHTTPS && !exists(d.Paths.LetsEncryptCert("zimam-"+string(p.Name))) {
		return conflict("Site %s has no certificate yet, so HTTPS can't be forced. Issue one first.", p.Name)
	}
	return nil
}

// securityFrom is the settings a site.security request asks for, as the
// state file keeps them; the users go to htpasswd only.
func securityFrom(p contract.SiteSecurityParams) Security {
	s := Security{
		ForceHTTPS:            p.ForceHTTPS,
		HSTSIncludeSubdomains: p.HSTS.IncludeSubdomains,
		HeadersPreset:         p.HeadersPreset,
		BlockRiskyFunctions:   p.BlockRiskyFunctions,
		RestrictToSiteFolder:  p.RestrictToSiteFolder,
		IPAllow:               p.IPAllow,
		IPDeny:                p.IPDeny,
		PasswordProtection:    p.PasswordProtection.Enabled,
	}
	if p.HSTS.MaxAge != nil {
		s.HSTSMaxAge = int(*p.HSTS.MaxAge)
	}
	return s
}

// securityData fills security.conf from a site's state. IP entries are
// parsed again, so the template gets the canonical prefix (D21).
func securityData(st State) (templates.SiteSecurity, error) {
	parse := func(l contract.IPList) ([]netip.Prefix, error) {
		var out []netip.Prefix
		for _, e := range l {
			p, err := contract.ParseIPEntry(e)
			if err != nil {
				return nil, fmt.Errorf("%s's IP entry %q %v", st.Name, e, err)
			}
			out = append(out, p)
		}
		return out, nil
	}
	allow, err := parse(st.Security.IPAllow)
	if err != nil {
		return templates.SiteSecurity{}, err
	}
	deny, err := parse(st.Security.IPDeny)
	if err != nil {
		return templates.SiteSecurity{}, err
	}
	return templates.SiteSecurity{
		Name:                  st.Name,
		HeadersPreset:         st.Security.HeadersPreset,
		HSTSMaxAge:            contract.HSTSMaxAge(st.Security.HSTSMaxAge),
		HSTSIncludeSubdomains: st.Security.HSTSIncludeSubdomains,
		IPDeny:                deny,
		IPAllow:               allow,
		PasswordProtection:    st.Security.PasswordProtection,
	}, nil
}

// siteFiles renders the files a site's security settings live in: its
// security.conf and vhost, and for a PHP site its pool.
func siteFiles(d sysexec.Deps, st State) (secConf, vhost, pool []byte, err error) {
	sec, err := securityData(st)
	if err != nil {
		return nil, nil, nil, err
	}
	if secConf, err = templates.Render("nginx/security.conf", sec); err != nil {
		return nil, nil, nil, err
	}
	https := exists(d.Paths.LetsEncryptCert("zimam-" + string(st.Name)))
	vhost, err = templates.Render("nginx/site.conf", templates.SiteVhost{
		Name: st.Name, Domain: st.Domain, PHPVersion: st.PHPVersion, HTTPS: https, ForceHTTPS: https && st.Security.ForceHTTPS,
	})
	if err != nil {
		return nil, nil, nil, err
	}
	if st.Type == contract.SitePHP {
		pool, err = templates.Render("php/site-pool.conf", templates.SitePool{
			Name: st.Name, PHPVersion: st.PHPVersion, BlockRiskyFunctions: st.Security.BlockRiskyFunctions, RestrictToSiteFolder: st.Security.RestrictToSiteFolder,
		})
	}
	return secConf, vhost, pool, err
}

// ApplySecurity applies a site's whole set of security settings (§9.6).
// security.conf, htpasswd, the vhost and, for a PHP site, the pool are one
// configtx transaction whose test is nginx -t and then php-fpm -t; if either
// fails, every file comes back. Then Nginx and PHP-FPM reload, and the state
// file takes the new set. The password hashes reach only htpasswd: they
// come in through spec.json, which the job system deletes after the run.
//
// Locks, in Task 26's order: the job holds the site's; php_<v> is held
// around the pool, and the reload lock around the test and Nginx's reload,
// then again by PHP-FPM's reload, never nested.
func ApplySecurity(ctx context.Context, d sysexec.Deps, p contract.SiteSecurityParams) (contract.SiteSecurityResult, error) {
	if err := CheckSecurity(d, p); err != nil {
		return contract.SiteSecurityResult{}, err
	}
	st, err := Load(d.Paths, p.Name)
	if err != nil {
		return contract.SiteSecurityResult{}, err
	}
	next := st
	next.Security = securityFrom(p)
	secConf, vhost, pool, err := siteFiles(d, next)
	if err != nil {
		return contract.SiteSecurityResult{}, err
	}
	gid, err := groupID(d.Paths.EtcGroup(), "www-data")
	if err != nil {
		return contract.SiteSecurityResult{}, err
	}
	htpasswd := configtx.Change{Path: d.Paths.SiteHtpasswd(p.Name), Remove: true}
	if p.PasswordProtection.Enabled {
		content, err := templates.Render("nginx/htpasswd", templates.Htpasswd{Users: p.PasswordProtection.Users})
		if err != nil {
			return contract.SiteSecurityResult{}, err
		}
		htpasswd = configtx.Change{Path: htpasswd.Path, Content: content, Mode: 0o640, UID: -1, GID: gid}
	}
	changes := []configtx.Change{
		configtx.RootFile(d.Paths.SiteSecurityConf(p.Name), secConf, 0o644),
		htpasswd,
		configtx.RootFile(d.Paths.NginxSiteAvailable(p.Name), vhost, 0o644),
	}
	if pool != nil {
		changes = append(changes, configtx.RootFile(d.Paths.PHPPool(st.PHPVersion, p.Name), pool, 0o644))
	}
	if !allApplied(changes) {
		if err := applySecurityFiles(ctx, d, next, changes); err != nil {
			return contract.SiteSecurityResult{}, err
		}
	}
	if !reflect.DeepEqual(st, next) {
		if err := Save(d.Paths, next); err != nil {
			return contract.SiteSecurityResult{}, err
		}
	}
	d.Logf("%s's security settings apply", p.Name)
	return contract.SiteSecurityResult{Name: p.Name}, nil
}

// applySecurityFiles is ApplySecurity's transaction and reloads.
func applySecurityFiles(ctx context.Context, d sysexec.Deps, st State, changes []configtx.Change) error {
	if err := os.MkdirAll(d.Paths.SiteNginxDir(st.Name), 0o755); err != nil {
		return err
	}
	isPHP := st.Type == contract.SitePHP
	if isPHP {
		unlock, err := sysexec.Lock(ctx, d.Paths.SubjectLock("php_"+string(st.PHPVersion)))
		if err != nil {
			return err
		}
		defer unlock()
		if err := checkPHPInstalled(d, st.PHPVersion); err != nil {
			return err
		}
	}
	d.Logf("Writing %s's security settings, then testing Nginx and PHP-FPM", st.Name)
	err := configtx.WithReloadLock(ctx, d.Paths, func() error {
		err := configtx.Apply(ctx, changes, func(ctx context.Context) error {
			if err := nginx.Test(ctx, d); err != nil {
				return err
			}
			if isPHP {
				return testPools(ctx, d, st.PHPVersion)
			}
			return nil
		})
		if err != nil {
			return err
		}
		d.Logf("Reloading Nginx")
		_, err = d.Run.Run(ctx, sysexec.Cmd{Path: systemctl, Args: []string{"reload-or-restart", "nginx"}})
		return err
	})
	if err != nil || !isPHP {
		return err
	}
	if err := php.ApplyFPM(ctx, d, st.PHPVersion); err != nil {
		return err
	}
	return waitForSocket(ctx, d.Paths.PHPSocket(st.Name, st.PHPVersion))
}

// allApplied reports whether every change is already on disk, content and
// mode, or already absent for a removal.
func allApplied(changes []configtx.Change) bool {
	for _, c := range changes {
		if c.Remove {
			if exists(c.Path) {
				return false
			}
			continue
		}
		fi, err := os.Lstat(c.Path)
		if err != nil || fi.Mode().Perm() != c.Mode.Perm() || !fileIs(c.Path, c.Content) {
			return false
		}
	}
	return true
}

// groupID reads a group's id from /etc/group, as getent would: htpasswd
// belongs to www-data's group (§5.9).
func groupID(path, group string) (int, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		f := strings.Split(sc.Text(), ":")
		if len(f) >= 3 && f[0] == group {
			return strconv.Atoi(f[2])
		}
	}
	if err := sc.Err(); err != nil {
		return 0, err
	}
	return 0, fmt.Errorf("%s has no group %s", path, group)
}
