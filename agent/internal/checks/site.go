package checks

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"sort"
	"strings"
	"syscall"
	"time"

	"zimam/agent/contract"
	"zimam/agent/internal/acme"
	"zimam/agent/internal/helper"
	"zimam/agent/internal/sites"
	"zimam/agent/internal/sysexec"
)

// Helper is what the site checks need of the site-user helper: the
// canary's write and removal, and the world-writable scan (§5.6).
type Helper interface {
	Write(ctx context.Context, a helper.Account, name string, mode fs.FileMode, content io.Reader) error
	RemoveTree(ctx context.Context, a helper.Account, name string) error
	ScanWorldWritable(ctx context.Context, a helper.Account, dir string) (helper.ScanResult, error)
}

// Env is what the checks run with.
type Env struct {
	D      sysexec.Deps
	Helper Helper
	Lookup func(username string) (helper.Account, error)
	Probe  Probe
}

const (
	certWarnDays = 14 // §9.7
	phpWarnDays  = 90 // §9.7
	firstPaths   = 20 // world-writable paths shown
)

// Site runs every site check of §9.7, in its order. A check that can't
// tell is inconclusive rather than an error, so one broken probe doesn't
// hide the others; a site without a state file is not_found.
func Site(ctx context.Context, e Env, name contract.SiteName) ([]contract.Finding, error) {
	st, err := sites.Load(e.D.Paths, name)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, &contract.AgentError{Code: contract.ErrNotFound, Message: fmt.Sprintf("Site %s doesn't exist.", name)}
	}
	if err != nil {
		return nil, err
	}
	acct, err := e.Lookup("s_" + string(name))
	if err != nil {
		return nil, fmt.Errorf("looking up s_%s: %w", name, err)
	}
	now := e.D.Now()
	return []contract.Finding{
		dotfilesBlocked(ctx, e, st, acct),
		configMatches(e, st),
		worldWritable(ctx, e, acct),
		phpSupported(st, now),
		poolUser(e, st),
		homePermissions(e, st, acct),
		certificateValid(e, st, now),
		httpsRedirect(ctx, e, st),
	}, nil
}

func finding(check string, status contract.FindingStatus, format string, args ...any) contract.Finding {
	return contract.Finding{Check: check, Status: status, Detail: fmt.Sprintf(format, args...)}
}

func hasCert(e Env, st sites.State) bool {
	_, err := os.Stat(e.D.Paths.LetsEncryptCert("zimam-" + string(st.Name)))
	return err == nil
}

// dotfilesBlocked asks for / first: a 401 or 403 there (password
// protection or IP rules) would make the canary's 403 prove nothing. Then
// the helper writes a canary dotfile into the public folder, which must
// answer 403, and removes it whatever happened.
func dotfilesBlocked(ctx context.Context, e Env, st sites.State, a helper.Account) contract.Finding {
	const check = "dotfiles_blocked"
	https := hasCert(e, st)
	code, _, err := e.Probe.Get(ctx, https, string(st.Domain), "/")
	switch {
	case err != nil:
		return finding(check, contract.FindingInconclusive, "The site didn't answer: %v", err)
	case code == http.StatusUnauthorized || code == http.StatusForbidden:
		return finding(check, contract.FindingInconclusive, "The site answers / with %d (password protection or IP rules), so a refused dotfile proves nothing.", code)
	}
	random := make([]byte, 8)
	if _, err := rand.Read(random); err != nil {
		return finding(check, contract.FindingInconclusive, "No random name for the canary: %v", err)
	}
	name := ".zimam-canary-" + hex.EncodeToString(random)
	rel := "site/current/public/" + name
	if err := e.Helper.Write(ctx, a, rel, 0o640, strings.NewReader("zimam canary\n")); err != nil {
		return finding(check, contract.FindingInconclusive, "The canary couldn't be written: %v", err)
	}
	defer func() {
		// A fresh context: the canary goes even when the check's has ended.
		rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		if err := e.Helper.RemoveTree(rctx, a, rel); err != nil {
			e.D.Logf("security.check: removing %s: %v", rel, err)
		}
	}()
	code, _, err = e.Probe.Get(ctx, https, string(st.Domain), "/"+name)
	switch {
	case err != nil:
		return finding(check, contract.FindingFail, "Requesting the canary failed: %v", err)
	case code != http.StatusForbidden:
		return finding(check, contract.FindingFail, "A dotfile in the public folder answered %d, not 403.", code)
	}
	return finding(check, contract.FindingPass, "A dotfile in the public folder answered 403.")
}

// configMatches renders the vhost, security.conf and pool from the state
// file again and compares bytes with the files on disk.
func configMatches(e Env, st sites.State) contract.Finding {
	const check = "config_matches_template"
	files, err := sites.ExpectedFiles(e.D, st)
	if err != nil {
		return finding(check, contract.FindingInconclusive, "Rendering the site's files failed: %v", err)
	}
	var differ []string
	for path, want := range files {
		got, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(got, want) {
			differ = append(differ, path)
		}
	}
	if len(differ) > 0 {
		sort.Strings(differ)
		return finding(check, contract.FindingFail, "Changed or missing since Zimam wrote them: %s.", strings.Join(differ, ", "))
	}
	return finding(check, contract.FindingPass, "The vhost, security.conf and pool match the template.")
}

// worldWritable scans site/ through the helper.
func worldWritable(ctx context.Context, e Env, a helper.Account) contract.Finding {
	const check = "no_world_writable_files"
	r, err := e.Helper.ScanWorldWritable(ctx, a, "site")
	if err != nil {
		return finding(check, contract.FindingInconclusive, "The scan failed: %v", err)
	}
	more := ""
	if r.Truncated {
		more = " The scan stopped early, so there may be more."
	}
	if r.Count == 0 {
		return finding(check, contract.FindingPass, "None among %d entries.%s", r.Scanned, more)
	}
	shown := r.Paths[:min(len(r.Paths), firstPaths)]
	return finding(check, contract.FindingFail, "%d world-writable entries; the first: %s.%s", r.Count, strings.Join(shown, ", "), more)
}

// phpSupported warns within 90 days of the version's end of security
// support and fails after it (§8.1).
func phpSupported(st sites.State, now time.Time) contract.Finding {
	const check = "php_version_supported"
	if st.Type != contract.SitePHP {
		return finding(check, contract.FindingPass, "A static site runs no PHP.")
	}
	for _, r := range contract.PHPAllowlist {
		if r.Version != st.PHPVersion {
			continue
		}
		end, err := time.Parse("2006-01-02", r.SecurityEnd)
		if err != nil {
			return finding(check, contract.FindingInconclusive, "PHP %s's end date %q: %v", r.Version, r.SecurityEnd, err)
		}
		switch {
		case !r.SupportedOn(now):
			return finding(check, contract.FindingFail, "PHP %s's security support ended on %s.", r.Version, r.SecurityEnd)
		case now.AddDate(0, 0, phpWarnDays).After(end):
			return finding(check, contract.FindingWarn, "PHP %s's security support ends on %s. Switch the site to a newer version.", r.Version, r.SecurityEnd)
		}
		return finding(check, contract.FindingPass, "PHP %s has security support until %s.", r.Version, r.SecurityEnd)
	}
	return finding(check, contract.FindingFail, "PHP %s isn't on the agent's list.", st.PHPVersion)
}

// poolUser reads the pool's user and group lines.
func poolUser(e Env, st sites.State) contract.Finding {
	const check = "pool_runs_as_site_user"
	if st.Type != contract.SitePHP {
		return finding(check, contract.FindingPass, "A static site has no PHP pool.")
	}
	b, err := os.ReadFile(e.D.Paths.PHPPool(st.PHPVersion, st.Name))
	if err != nil {
		return finding(check, contract.FindingFail, "The pool can't be read: %v", err)
	}
	want := "s_" + string(st.Name)
	seen := map[string]string{}
	for line := range strings.Lines(string(b)) {
		k, v, ok := strings.Cut(line, "=")
		if ok {
			seen[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	if seen["user"] != want || seen["group"] != want {
		return finding(check, contract.FindingFail, "The pool runs as user %q and group %q, not %s.", seen["user"], seen["group"], want)
	}
	return finding(check, contract.FindingPass, "The pool runs as %s.", want)
}

// homePermissions stats the home itself, which lives in /home, not inside
// it: mode 0750 and owned by the site user.
func homePermissions(e Env, st sites.State, a helper.Account) contract.Finding {
	const check = "home_permissions"
	fi, err := os.Lstat(e.D.Paths.SiteHome(st.Name))
	if err != nil {
		return finding(check, contract.FindingFail, "The home can't be read: %v", err)
	}
	sys, ok := fi.Sys().(*syscall.Stat_t)
	switch {
	case !fi.IsDir():
		return finding(check, contract.FindingFail, "The home isn't a directory.")
	case fi.Mode().Perm() != 0o750:
		return finding(check, contract.FindingFail, "The home's mode is %04o, not 0750.", fi.Mode().Perm())
	case !ok || sys.Uid != a.UID:
		return finding(check, contract.FindingFail, "The home isn't owned by %s.", a.Name)
	}
	return finding(check, contract.FindingPass, "The home is 0750 and owned by %s.", a.Name)
}

// certificateValid reads the certificate file's expiry: a warning within
// 14 days, a failure once expired or when HTTPS is expected without one.
// The expiry travels in the finding, so the panel learns of renewals.
func certificateValid(e Env, st sites.State, now time.Time) contract.Finding {
	const check = "certificate_valid"
	path := e.D.Paths.LetsEncryptCert("zimam-" + string(st.Name))
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		if st.Security.ForceHTTPS {
			return finding(check, contract.FindingFail, "HTTPS is forced, but the site has no certificate.")
		}
		return finding(check, contract.FindingPass, "No certificate yet; the site is served over HTTP.")
	}
	expiry, err := acme.ReadExpiry(path)
	if err != nil {
		return finding(check, contract.FindingInconclusive, "The certificate can't be read: %v", err)
	}
	expiry = expiry.UTC()
	var f contract.Finding
	switch {
	case !expiry.After(now):
		f = finding(check, contract.FindingFail, "The certificate expired on %s.", expiry.Format(time.DateOnly))
	case expiry.Before(now.AddDate(0, 0, certWarnDays)):
		f = finding(check, contract.FindingWarn, "The certificate expires on %s, and renewal hasn't replaced it.", expiry.Format(time.DateOnly))
	default:
		f = finding(check, contract.FindingPass, "The certificate is valid until %s.", expiry.Format(time.DateOnly))
	}
	f.ExpiresAt = &expiry
	return f
}

// httpsRedirect asks for / over HTTP when force_https is on: it must
// answer 301 to an https:// address.
func httpsRedirect(ctx context.Context, e Env, st sites.State) contract.Finding {
	const check = "https_redirect"
	if !st.Security.ForceHTTPS {
		return finding(check, contract.FindingPass, "HTTPS isn't forced, so HTTP serves the site.")
	}
	code, location, err := e.Probe.Get(ctx, false, string(st.Domain), "/")
	switch {
	case err != nil:
		return finding(check, contract.FindingInconclusive, "The site didn't answer over HTTP: %v", err)
	case code != http.StatusMovedPermanently || !strings.HasPrefix(location, "https://"):
		return finding(check, contract.FindingFail, "HTTP answered %d %s, not a 301 to https://.", code, location)
	}
	return finding(check, contract.FindingPass, "HTTP answers 301 to %s.", location)
}
