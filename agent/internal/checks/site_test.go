package checks

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"zimam/agent/contract"
	"zimam/agent/internal/helper"
	"zimam/agent/internal/paths"
	"zimam/agent/internal/sites"
	"zimam/agent/internal/sysexec"
)

var testNow = time.Date(2026, 11, 3, 9, 0, 0, 0, time.UTC)

// fakeHelper records what goes through the helper; the canary it writes
// is visible to the test server until removed.
type fakeHelper struct {
	mu       sync.Mutex
	files    map[string]bool
	ops      []string
	writeErr error
	scan     helper.ScanResult
}

func (h *fakeHelper) Write(_ context.Context, a helper.Account, name string, _ fs.FileMode, r io.Reader) error {
	io.Copy(io.Discard, r)
	h.mu.Lock()
	defer h.mu.Unlock()
	h.ops = append(h.ops, a.Name+" write "+name)
	if h.writeErr != nil {
		return h.writeErr
	}
	h.files[name] = true
	return nil
}

func (h *fakeHelper) RemoveTree(_ context.Context, a helper.Account, name string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.ops = append(h.ops, a.Name+" remove-tree "+name)
	delete(h.files, name)
	return nil
}

func (h *fakeHelper) ScanWorldWritable(_ context.Context, a helper.Account, dir string) (helper.ScanResult, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.ops = append(h.ops, a.Name+" scan-world-writable "+dir)
	return h.scan, nil
}

func (h *fakeHelper) has(name string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.files[name]
}

type siteEnv struct {
	t    *testing.T
	e    Env
	h    *fakeHelper
	st   sites.State
	acct helper.Account
}

// newSiteEnv is the PHP site shop on 8.4 with its files rendered as its
// state file says and its home 0750, served by handler over HTTP.
func newSiteEnv(t *testing.T, handler http.HandlerFunc) *siteEnv {
	t.Helper()
	p := paths.Paths{Root: t.TempDir()}
	d := sysexec.Deps{Paths: p, Run: sysexec.NewFakeRunner(), Logf: t.Logf, Now: func() time.Time { return testNow }}
	st := sites.State{Name: "shop", Domain: "shop.example.com", Type: contract.SitePHP, PHPVersion: "8.4", TemplateVersion: 1, Security: sites.DefaultSecurity()}
	if err := sites.Save(p, st); err != nil {
		t.Fatal(err)
	}
	files, err := sites.ExpectedFiles(d, st)
	if err != nil {
		t.Fatal(err)
	}
	for path, b := range files {
		write(t, path, string(b))
	}
	if err := os.MkdirAll(p.SiteHome("shop"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(p.SiteHome("shop"), 0o750); err != nil {
		t.Fatal(err)
	}
	h := &fakeHelper{files: map[string]bool{}}
	acct := helper.Account{Name: "s_shop", UID: uint32(os.Getuid()), GID: uint32(os.Getgid()), Home: "/home/s_shop"}
	se := &siteEnv{t: t, h: h, st: st, acct: acct}
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	se.e = Env{D: d, Helper: h, Lookup: func(string) (helper.Account, error) { return acct, nil }, Probe: Probe{HTTP: strings.TrimPrefix(srv.URL, "http://")}}
	return se
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// nginxLike answers as the site's vhost does: 403 for dotfiles, 200 else.
func (se *siteEnv) nginxLike(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/.") {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func wantStatus(t *testing.T, f contract.Finding, check string, status contract.FindingStatus) {
	t.Helper()
	if f.Check != check || f.Status != status {
		t.Errorf("finding = %+v, want %s %s", f, check, status)
	}
}

const canaryDir = "site/current/public/"

func TestDotfilesBlocked(t *testing.T) {
	var se *siteEnv
	var canarySeen bool
	se = newSiteEnv(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Host != "shop.example.com" {
			t.Errorf("Host %q", r.Host)
		}
		if strings.HasPrefix(r.URL.Path, "/.zimam-canary-") {
			canarySeen = se.h.has(strings.TrimPrefix(canaryDir+r.URL.Path[1:], ""))
		}
		se.nginxLike(w, r)
	})
	f := dotfilesBlocked(context.Background(), se.e, se.st, se.acct)
	wantStatus(t, f, "dotfiles_blocked", contract.FindingPass)
	if !canarySeen {
		t.Error("the canary wasn't there while it was requested")
	}
	if len(se.h.files) != 0 {
		t.Errorf("the canary was left: %v", se.h.files)
	}
	// A dotfile served as 200 fails.
	se2 := newSiteEnv(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	wantStatus(t, dotfilesBlocked(context.Background(), se2.e, se2.st, se2.acct), "dotfiles_blocked", contract.FindingFail)
}

// Password protection or IP rules answer / with 401 or 403, and then a 403
// for the canary proves nothing.
func TestDotfilesInconclusiveOn401(t *testing.T) {
	for _, code := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		se := newSiteEnv(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(code) })
		wantStatus(t, dotfilesBlocked(context.Background(), se.e, se.st, se.acct), "dotfiles_blocked", contract.FindingInconclusive)
		if len(se.h.ops) != 0 {
			t.Errorf("%d: wrote a canary anyway: %v", code, se.h.ops)
		}
	}
}

// The canary goes even when the request for it fails.
func TestCanaryRemovedOnFailure(t *testing.T) {
	var se *siteEnv
	se = newSiteEnv(t, func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/.zimam-canary-") {
			hj, _ := w.(http.Hijacker)
			c, _, _ := hj.Hijack()
			c.Close() // the connection drops mid-request
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	f := dotfilesBlocked(context.Background(), se.e, se.st, se.acct)
	if f.Status == contract.FindingPass {
		t.Errorf("finding = %+v", f)
	}
	if len(se.h.files) != 0 {
		t.Errorf("the canary was left: %v", se.h.files)
	}
	var removed bool
	for _, op := range se.h.ops {
		removed = removed || strings.HasPrefix(op, "s_shop remove-tree "+canaryDir+".zimam-canary-")
	}
	if !removed {
		t.Errorf("ops = %v, want the canary removed", se.h.ops)
	}
}

func TestConfigDrift(t *testing.T) {
	se := newSiteEnv(t, nil)
	wantStatus(t, configMatches(se.e, se.st), "config_matches_template", contract.FindingPass)
	write(t, se.e.D.Paths.SiteSecurityConf("shop"), "# edited by hand\n")
	f := configMatches(se.e, se.st)
	wantStatus(t, f, "config_matches_template", contract.FindingFail)
	if !strings.Contains(f.Detail, "/etc/nginx/zimam/shop/security.conf") {
		t.Errorf("detail %q doesn't name the file", f.Detail)
	}
	os.Remove(se.e.D.Paths.PHPPool("8.4", "shop"))
	if f := configMatches(se.e, se.st); !strings.Contains(f.Detail, "zimam-shop.conf") {
		t.Errorf("a missing pool isn't reported: %q", f.Detail)
	}
}

func TestWorldWritableFirst20(t *testing.T) {
	se := newSiteEnv(t, nil)
	wantStatus(t, worldWritable(context.Background(), se.e, se.acct), "no_world_writable_files", contract.FindingPass)
	var paths []string
	for i := range 25 {
		paths = append(paths, fmt.Sprintf("site/f%02d", i))
	}
	se.h.scan = helper.ScanResult{Count: 25, Paths: paths, Scanned: 100}
	f := worldWritable(context.Background(), se.e, se.acct)
	wantStatus(t, f, "no_world_writable_files", contract.FindingFail)
	if !strings.Contains(f.Detail, "25 ") || !strings.Contains(f.Detail, "site/f19") || strings.Contains(f.Detail, "site/f20") {
		t.Errorf("detail = %q, want the count and the first 20 paths", f.Detail)
	}
	if se.h.ops[len(se.h.ops)-1] != "s_shop scan-world-writable site" {
		t.Errorf("ops = %v", se.h.ops)
	}
}

func TestPhpVersionWarn90Days(t *testing.T) {
	st := sites.State{Name: "shop", Type: contract.SitePHP, PHPVersion: "8.2"} // support ends 2026-12-31
	for _, tc := range []struct {
		now  time.Time
		want contract.FindingStatus
	}{
		{time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC), contract.FindingPass},
		{time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC), contract.FindingWarn},
		{time.Date(2026, 12, 31, 23, 0, 0, 0, time.UTC), contract.FindingWarn},
		{time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC), contract.FindingFail},
	} {
		f := phpSupported(st, tc.now)
		if f.Status != tc.want {
			t.Errorf("on %s: %+v, want %s", tc.now.Format("2006-01-02"), f, tc.want)
		}
	}
	if f := phpSupported(sites.State{Name: "docs", Type: contract.SiteStatic}, testNow); f.Status != contract.FindingPass {
		t.Errorf("a static site: %+v", f)
	}
}

func TestCertificateWarnAndFail(t *testing.T) {
	se := newSiteEnv(t, nil)
	// No certificate and none expected: nothing wrong.
	wantStatus(t, certificateValid(se.e, se.st, testNow), "certificate_valid", contract.FindingPass)
	forced := se.st
	forced.Security.ForceHTTPS = true
	wantStatus(t, certificateValid(se.e, forced, testNow), "certificate_valid", contract.FindingFail)

	cert := se.e.D.Paths.LetsEncryptCert("zimam-shop")
	for _, tc := range []struct {
		expires time.Time
		want    contract.FindingStatus
	}{
		{testNow.AddDate(0, 0, 60), contract.FindingPass},
		{testNow.AddDate(0, 0, 10), contract.FindingWarn},
		{testNow.Add(-time.Hour), contract.FindingFail},
	} {
		writeTestCert(t, cert, tc.expires)
		f := certificateValid(se.e, se.st, testNow)
		if f.Status != tc.want || f.ExpiresAt == nil || !f.ExpiresAt.Equal(tc.expires.Truncate(time.Second)) {
			t.Errorf("expiring %s: %+v, want %s with expires_at", tc.expires, f, tc.want)
		}
	}
}

func TestHttpsRedirect(t *testing.T) {
	se := newSiteEnv(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://shop.example.com/", http.StatusMovedPermanently)
	})
	// Not forced: nothing to check.
	wantStatus(t, httpsRedirect(context.Background(), se.e, se.st), "https_redirect", contract.FindingPass)
	forced := se.st
	forced.Security.ForceHTTPS = true
	wantStatus(t, httpsRedirect(context.Background(), se.e, forced), "https_redirect", contract.FindingPass)

	plain := newSiteEnv(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	wantStatus(t, httpsRedirect(context.Background(), plain.e, forced), "https_redirect", contract.FindingFail)
	elsewhere := newSiteEnv(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://shop.example.com/x", http.StatusMovedPermanently)
	})
	wantStatus(t, httpsRedirect(context.Background(), elsewhere.e, forced), "https_redirect", contract.FindingFail)
}

func TestPoolUser(t *testing.T) {
	se := newSiteEnv(t, nil)
	wantStatus(t, poolUser(se.e, se.st), "pool_runs_as_site_user", contract.FindingPass)
	pool := se.e.D.Paths.PHPPool("8.4", "shop")
	b, _ := os.ReadFile(pool)
	write(t, pool, strings.Replace(string(b), "user = s_shop", "user = www-data", 1))
	wantStatus(t, poolUser(se.e, se.st), "pool_runs_as_site_user", contract.FindingFail)
	os.Remove(pool)
	wantStatus(t, poolUser(se.e, se.st), "pool_runs_as_site_user", contract.FindingFail)
}

func TestHomePermissions(t *testing.T) {
	se := newSiteEnv(t, nil)
	wantStatus(t, homePermissions(se.e, se.st, se.acct), "home_permissions", contract.FindingPass)
	os.Chmod(se.e.D.Paths.SiteHome("shop"), 0o755)
	wantStatus(t, homePermissions(se.e, se.st, se.acct), "home_permissions", contract.FindingFail)
	os.Chmod(se.e.D.Paths.SiteHome("shop"), 0o750)
	other := se.acct
	other.UID++
	wantStatus(t, homePermissions(se.e, se.st, other), "home_permissions", contract.FindingFail)
}

// Site runs every check, in §9.7's order; a site without a state file is
// not_found.
func TestSiteRunsEveryCheck(t *testing.T) {
	var se *siteEnv
	se = newSiteEnv(t, func(w http.ResponseWriter, r *http.Request) { se.nginxLike(w, r) })
	got, err := Site(context.Background(), se.e, "shop")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, f := range got {
		names = append(names, f.Check)
	}
	want := "dotfiles_blocked config_matches_template no_world_writable_files php_version_supported pool_runs_as_site_user home_permissions certificate_valid https_redirect"
	if strings.Join(names, " ") != want {
		t.Errorf("checks = %v", names)
	}
	_, err = Site(context.Background(), se.e, "blog")
	var ae *contract.AgentError
	if !errors.As(err, &ae) || ae.Code != contract.ErrNotFound {
		t.Errorf("unknown site: %v", err)
	}
}
