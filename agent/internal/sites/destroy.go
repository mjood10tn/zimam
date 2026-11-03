package sites

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"time"

	"zimam/agent/contract"
	"zimam/agent/internal/helper"
	"zimam/agent/internal/nginx"
	"zimam/agent/internal/sysexec"
)

const (
	pkill    = "/usr/bin/pkill"
	pgrep    = "/usr/bin/pgrep"
	userdel  = "/usr/sbin/userdel"
	groupdel = "/usr/sbin/groupdel"
	certbot  = "/usr/bin/certbot"
)

// processWait is how long the site user's processes get to end after
// SIGKILL. A variable, so tests can shorten it.
var processWait = 5 * time.Second

// CheckDestroy is site.destroy's precheck: not_found for a site with
// nothing on the server. A site whose provisioning failed has no state
// file yet but can still be destroyed (§9.2), so any of its traces counts.
func CheckDestroy(d sysexec.Deps, lookup Lookup, name contract.SiteName) error {
	found, err := siteTraces(d, lookup, name)
	if err != nil {
		return err
	}
	if !found {
		return &contract.AgentError{Code: contract.ErrNotFound, Message: fmt.Sprintf("Site %s doesn't exist.", name)}
	}
	return nil
}

func siteTraces(d sysexec.Deps, lookup Lookup, name contract.SiteName) (bool, error) {
	p := d.Paths
	for _, path := range []string{
		p.SiteState(name), p.SiteHome(name), p.NginxSiteEnabled(name), p.NginxSiteAvailable(name), p.SiteNginxDir(name),
		p.SiteLogDir(name), p.SiteLogrotateConf(name), p.LetsEncryptCert("zimam-" + string(name)), p.CertRecord(string(name)),
	} {
		if exists(path) {
			return true, nil
		}
	}
	for _, r := range contract.PHPAllowlist {
		if exists(p.PHPPool(r.Version, name)) {
			return true, nil
		}
	}
	username := "s_" + string(name)
	if _, err := lookup(username); err == nil {
		return true, nil
	} else if !errors.As(err, new(user.UnknownUserError)) {
		return false, err
	}
	return groupExists(p.EtcGroup(), username)
}

// Destroy removes a site in §9.8's order: Nginx stops serving it, its pools
// go, its user's processes are killed, the helper empties its home as the
// site user, and root removes the empty home, the site's logs, Nginx
// directory, log rules, certificate, user, group and, last, its state file.
// Each step skips what is already gone, so a failed destroy is finished by
// running it again.
func Destroy(ctx context.Context, d sysexec.Deps, h Helper, lookup Lookup, name contract.SiteName) (contract.SiteNameResult, error) {
	if err := name.Validate(); err != nil {
		return contract.SiteNameResult{}, fmt.Errorf("site %q %v", name, err)
	}
	p := d.Paths
	username := "s_" + string(name)

	// Step 1: Nginx.
	if err := nginx.DisableSite(ctx, d, nginx.Link{Path: p.NginxSiteEnabled(name), Target: p.NginxSiteAvailable(name)}); err != nil {
		return contract.SiteNameResult{}, err
	}
	if err := removeFile(p.NginxSiteAvailable(name)); err != nil {
		return contract.SiteNameResult{}, err
	}

	// Step 2: every pool the site has, and the version the state file names
	// even without one, so a rerun still stops an FPM left with no pool.
	versions := map[contract.PHPVersion]bool{}
	if st, err := Load(p, name); err == nil && st.Type == contract.SitePHP {
		versions[st.PHPVersion] = true
	} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return contract.SiteNameResult{}, err
	}
	for _, r := range contract.PHPAllowlist {
		if versions[r.Version] || exists(p.PHPPool(r.Version, name)) {
			if err := removePool(ctx, d, name, r.Version, 0); err != nil {
				return contract.SiteNameResult{}, err
			}
		}
	}

	a, err := lookup(username)
	userExists := err == nil
	if err != nil && !errors.As(err, new(user.UnknownUserError)) {
		return contract.SiteNameResult{}, err
	}
	if userExists {
		// Step 3: nothing of the site user may run while its home goes.
		if err := killAll(ctx, d, username); err != nil {
			return contract.SiteNameResult{}, err
		}
		// Step 4: the helper empties the home as the site user, inside an
		// os.Root, so a symlink planted there leads nowhere outside it.
		if exists(p.SiteHome(name)) {
			d.Logf("Removing everything in %s's home", name)
			if err := h.RemoveTree(ctx, a, "."); err != nil {
				return contract.SiteNameResult{}, err
			}
		}
	}

	// Step 5, as root.
	if err := removeHome(p.SiteHome(name)); err != nil {
		return contract.SiteNameResult{}, err
	}
	for _, dir := range []string{p.SiteLogDir(name), p.SiteNginxDir(name)} {
		if err := os.RemoveAll(dir); err != nil {
			return contract.SiteNameResult{}, err
		}
	}
	if err := removeFile(p.SiteLogrotateConf(name)); err != nil {
		return contract.SiteNameResult{}, err
	}
	if exists(p.LetsEncryptCert("zimam-" + string(name))) {
		d.Logf("Deleting %s's certificate", name)
		if _, err := d.Run.Run(ctx, sysexec.Cmd{Path: certbot, Args: []string{"delete", "--cert-name", "zimam-" + string(name), "--non-interactive"}}); err != nil {
			return contract.SiteNameResult{}, err
		}
	}
	if err := removeFile(p.CertRecord(string(name))); err != nil {
		return contract.SiteNameResult{}, err
	}
	if userExists {
		d.Logf("Deleting the user %s", username)
		if _, err := d.Run.Run(ctx, sysexec.Cmd{Path: userdel, Args: []string{username}}); err != nil {
			return contract.SiteNameResult{}, err
		}
	}
	// userdel removes the user's own group when nobody else is in it; with
	// www-data in it, the group is left for groupdel.
	if ok, err := groupExists(p.EtcGroup(), username); err != nil {
		return contract.SiteNameResult{}, err
	} else if ok {
		if _, err := d.Run.Run(ctx, sysexec.Cmd{Path: groupdel, Args: []string{username}}); err != nil {
			return contract.SiteNameResult{}, err
		}
	}
	if err := removeFile(p.SiteState(name)); err != nil {
		return contract.SiteNameResult{}, err
	}
	d.Logf("Site %s is deleted", name)
	return contract.SiteNameResult{Name: name}, nil
}

// killAll sends SIGKILL to every process of username, then waits until pgrep
// finds none. pkill and pgrep exit 1 when nothing matches.
func killAll(ctx context.Context, d sysexec.Deps, username string) error {
	d.Logf("Stopping every process of %s", username)
	if _, err := d.Run.Run(ctx, sysexec.Cmd{Path: pkill, Args: []string{"-KILL", "-u", username}}); err != nil && !exitCode(err, 1) {
		return err
	}
	deadline := time.Now().Add(processWait)
	for {
		_, err := d.Run.Run(ctx, sysexec.Cmd{Path: pgrep, Args: []string{"-u", username}})
		switch {
		case exitCode(err, 1):
			return nil
		case err != nil:
			return err
		case time.Now().After(deadline):
			return fmt.Errorf("processes of %s were still running %s after SIGKILL", username, processWait)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func exitCode(err error, code int) bool {
	var exit *sysexec.ExitError
	return errors.As(err, &exit) && exit.Code == code
}

// removeHome removes the emptied home. rmdir refuses one with anything
// left in it, and the job fails rather than root deleting inside it.
func removeHome(path string) error {
	fi, err := os.Lstat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil
	case err != nil:
		return err
	case !fi.IsDir():
		return fmt.Errorf("%s isn't a directory, so it isn't removed", path)
	}
	if err := os.Remove(path); err != nil {
		names, _ := os.ReadDir(path)
		if len(names) > 0 {
			return fmt.Errorf("%s isn't empty after the helper emptied it (%s is left), so it isn't removed", path, filepath.Join(path, names[0].Name()))
		}
		return err
	}
	return nil
}

func removeFile(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// groupExists reports whether /etc/group has a line for group.
func groupExists(path, group string) (bool, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	for line := range strings.Lines(string(data)) {
		if strings.HasPrefix(line, group+":") {
			return true, nil
		}
	}
	return false, nil
}

var _ Helper = (*helper.Client)(nil)
