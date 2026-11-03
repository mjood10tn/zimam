package sites

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"zimam/agent/contract"
	"zimam/agent/internal/templates"
)

var (
	static = contract.SiteProvisionParams{Name: "shop", Domain: "shop.example.com", Type: contract.SiteStatic}
	php84  = contract.SiteProvisionParams{Name: "shop", Domain: "shop.example.com", Type: contract.SitePHP, PHPVersion: "8.4"}
)

const (
	useraddCmd   = "/usr/sbin/useradd -M -d /home/s_shop -s /usr/sbin/nologin -U s_shop"
	usermodCmd   = "/usr/sbin/usermod -aG s_shop www-data"
	nginxTest    = "/usr/sbin/nginx -t"
	nginxReload  = "/usr/bin/systemctl reload-or-restart nginx"
	fpmTest84    = "/usr/sbin/php-fpm8.4 -t"
	fpmActive84  = "/usr/bin/systemctl is-active --quiet php8.4-fpm"
	fpmEnable84  = "/usr/bin/systemctl enable --now php8.4-fpm"
	fpmReload84  = "/usr/bin/systemctl reload php8.4-fpm"
	helperUser   = "s_shop"
	placeholder  = "site/releases/initial/public"
	currentLink  = "site/current"
	initialRelTo = "releases/initial"
)

func (e *env) logrotateTest() string {
	return "/usr/sbin/logrotate -d --state " + e.p.LogrotateState() + " " + e.p.LogrotateConf()
}

func TestProvisionStaticSequence(t *testing.T) {
	e := newEnv(t)
	res, err := e.provision(static)
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	if res != (contract.SiteProvisionResult{Name: "shop", TemplateVersion: templates.Version}) {
		t.Errorf("result = %+v", res)
	}
	want := []string{useraddCmd, usermodCmd, nginxTest, nginxReload, e.logrotateTest()}
	if got := e.calls(); !slices.Equal(got, want) {
		t.Errorf("ran\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	wantOps := []string{
		helperUser + " mkdir " + placeholder + " 750",
		helperUser + " write " + placeholder + "/index.html 640",
		helperUser + " symlink-swap " + currentLink + " " + initialRelTo,
		helperUser + " mkdir logs 750",
		helperUser + " mkdir tmp 700",
	}
	if got := e.help.changes(); !slices.Equal(got, wantOps) {
		t.Errorf("helper\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(wantOps, "\n"))
	}
	if !strings.Contains(string(e.help.nodes[placeholder+"/index.html"].content), "Zimam site shop is ready") {
		t.Error("the placeholder isn't the static one")
	}
	for _, path := range []string{e.p.SiteSecurityConf("shop"), e.p.NginxSiteAvailable("shop"), e.p.SiteLogrotateConf("shop"), e.p.SiteState("shop")} {
		if !exists(path) {
			t.Errorf("%s wasn't written", path)
		}
	}
	if target, err := os.Readlink(e.p.NginxSiteEnabled("shop")); err != nil || target != e.p.NginxSiteAvailable("shop") {
		t.Errorf("sites-enabled link = %q, %v", target, err)
	}
	checkMode(t, e.p.SiteHome("shop"), 0o750|os.ModeDir)
	checkMode(t, e.p.SiteLogDir("shop"), 0o750|os.ModeDir)
	if exists(e.p.PHPPool("8.4", "shop")) {
		t.Error("a static site got a pool")
	}
}

func TestProvisionPHPSequence(t *testing.T) {
	e := newEnv(t)
	e.fpmActive("8.4", false)
	if _, err := e.provision(php84); err != nil {
		t.Fatalf("Provision: %v", err)
	}
	want := []string{useraddCmd, usermodCmd, fpmTest84, fpmTest84, fpmActive84, fpmEnable84, nginxTest, nginxReload, e.logrotateTest()}
	if got := e.calls(); !slices.Equal(got, want) {
		t.Errorf("ran\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	pool, err := os.ReadFile(e.p.PHPPool("8.4", "shop"))
	if err != nil || !strings.Contains(string(pool), "listen = /run/php/zimam-shop-8.4.sock") {
		t.Errorf("pool = %q, %v", pool, err)
	}
	vhost, _ := os.ReadFile(e.p.NginxSiteAvailable("shop"))
	if !strings.Contains(string(vhost), "fastcgi_pass unix:/run/php/zimam-shop-8.4.sock;") {
		t.Errorf("vhost doesn't pass PHP to the pool:\n%s", vhost)
	}
	if !strings.Contains(string(e.help.nodes[placeholder+"/index.php"].content), "PHP_VERSION") {
		t.Error("the placeholder isn't the PHP one")
	}
}

// The first pool on a version starts its FPM service; a later one reloads
// it, so the sites already on it keep their in-flight requests (§8.3).
func TestFirstPoolStartsFPM(t *testing.T) {
	e := newEnv(t)
	e.fpmActive("8.4", false)
	if _, err := e.provision(php84); err != nil {
		t.Fatal(err)
	}
	if got := e.calls(); !slices.Contains(got, fpmEnable84) || slices.Contains(got, fpmReload84) {
		t.Errorf("ran %q, want enable --now and no reload", got)
	}
}

func TestSecondPoolReloadsFPM(t *testing.T) {
	e := newEnv(t)
	writeFile(t, e.p.PHPPool("8.4", "blog"), "[zimam-blog]\n")
	e.fpmActive("8.4", true)
	if _, err := e.provision(php84); err != nil {
		t.Fatal(err)
	}
	if got := e.calls(); !slices.Contains(got, fpmReload84) || slices.Contains(got, fpmEnable84) {
		t.Errorf("ran %q, want a reload and no enable", got)
	}
}

// A provision that failed at step 5 is retried: steps 1–4 find their work
// done and change nothing; only reads run before Nginx is tried again.
func TestProvisionRetryResumes(t *testing.T) {
	e := newEnv(t)
	e.fpmActive("8.4", false, true)
	failNginx(e)
	if _, err := e.provision(php84); err == nil {
		t.Fatal("Provision passed with nginx -t failing")
	}
	if exists(e.p.SiteState("shop")) {
		t.Error("a failed provision wrote the state file")
	}
	calls, ops := len(e.run.Calls()), len(e.help.changes())

	if _, err := e.provision(php84); err != nil {
		t.Fatalf("the retry: %v", err)
	}
	want := []string{fpmActive84, nginxTest, nginxReload, e.logrotateTest()}
	if got := e.callsSince(calls); !slices.Equal(got, want) {
		t.Errorf("the retry ran\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if got := e.help.changes()[ops:]; len(got) != 0 {
		t.Errorf("the retry changed the home again: %q", got)
	}
	if !exists(e.p.SiteState("shop")) {
		t.Error("the retry wrote no state file")
	}
}

// failNginx makes the first nginx -t fail; later ones pass.
func failNginx(e *env) {
	e.failOnce[nginxTest] = "nginx: [emerg] unknown directive \"bogus\""
}

// Root creates the home only where nothing is, or where the site user's own
// directory already is: never through a symlink someone planted.
func TestHomeCreatedSafely(t *testing.T) {
	e := newEnv(t)
	elsewhere := filepath.Join(e.p.Root, "elsewhere")
	mkdir(t, elsewhere)
	if err := os.Symlink(elsewhere, e.p.SiteHome("shop")); err != nil {
		t.Fatal(err)
	}
	if _, err := e.provision(static); err == nil {
		t.Fatal("Provision passed with the home a symlink")
	}
	fi, err := os.Stat(elsewhere)
	if err != nil || fi.Mode().Perm() != 0o755 {
		t.Errorf("the symlink's target changed: %v, %v", fi.Mode(), err)
	}
	if len(e.help.changes()) != 0 {
		t.Errorf("the helper ran: %q", e.help.changes())
	}

	e = newEnv(t)
	writeFile(t, e.p.SiteHome("shop"), "a file")
	if _, err := e.provision(static); err == nil {
		t.Fatal("Provision passed with the home a regular file")
	}
}

// Root writes nothing under the home but the home itself (§5.6): the site's
// files all go through the helper, as the site user.
func TestSiteFilesWrittenOnlyThroughHelper(t *testing.T) {
	e := newEnv(t)
	e.fpmActive("8.4", false)
	if _, err := e.provision(php84); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(e.p.SiteHome("shop"))
	if err != nil || len(entries) != 0 {
		t.Errorf("root wrote %v under the home (%v)", entries, err)
	}
	for _, op := range e.help.ops {
		if !strings.HasPrefix(op, helperUser+" ") {
			t.Errorf("helper op %q didn't run as %s", op, helperUser)
		}
	}
}

// When Nginx rejects the site, its vhost, security.conf and sites-enabled
// link are gone again, so the rest of Nginx keeps working.
func TestNginxFailureRollsBackSiteFiles(t *testing.T) {
	e := newEnv(t)
	failNginx(e)
	_, err := e.provision(static)
	if err == nil || !strings.Contains(err.Error(), "unknown directive") {
		t.Fatalf("Provision = %v, want nginx -t's output", err)
	}
	for _, path := range []string{e.p.NginxSiteAvailable("shop"), e.p.NginxSiteEnabled("shop"), e.p.SiteSecurityConf("shop"), e.p.SiteState("shop")} {
		if exists(path) {
			t.Errorf("%s is left after Nginx refused the site", path)
		}
	}
}

func TestStateFileWritten(t *testing.T) {
	e := newEnv(t)
	e.fpmActive("8.4", false)
	if _, err := e.provision(php84); err != nil {
		t.Fatal(err)
	}
	checkMode(t, e.p.SiteState("shop"), 0o600)
	checkMode(t, e.p.SitesStateDir(), 0o700|os.ModeDir)
	st, err := Load(e.p, "shop")
	if err != nil {
		t.Fatal(err)
	}
	want := State{Name: "shop", Domain: "shop.example.com", Type: contract.SitePHP, PHPVersion: "8.4", TemplateVersion: templates.Version, Security: DefaultSecurity()}
	if !reflect.DeepEqual(st, want) {
		t.Errorf("state = %+v, want %+v", st, want)
	}
}

func TestDomainConflict(t *testing.T) {
	e := newEnv(t)
	if err := Save(e.p, State{Name: "blog", Domain: "shop.example.com", Type: contract.SiteStatic, TemplateVersion: 1, Security: DefaultSecurity()}); err != nil {
		t.Fatal(err)
	}
	wantCode(t, Check(e.d, static), contract.ErrConflict)

	panel := static
	panel.Domain = "panel.example.com"
	wantCode(t, Check(e.d, panel), contract.ErrConflict)
}

// A site that exists may be provisioned again only with the same domain,
// type and PHP version: a retry, not a change.
func TestExistingSiteConflict(t *testing.T) {
	e := newEnv(t)
	if err := Save(e.p, State{Name: "shop", Domain: "shop.example.com", Type: contract.SitePHP, PHPVersion: "8.4", TemplateVersion: 1, Security: DefaultSecurity()}); err != nil {
		t.Fatal(err)
	}
	if err := Check(e.d, php84); err != nil {
		t.Errorf("a retry with the same params: %v", err)
	}
	for _, p := range []contract.SiteProvisionParams{
		{Name: "shop", Domain: "other.example.com", Type: contract.SitePHP, PHPVersion: "8.4"},
		static,
		{Name: "shop", Domain: "shop.example.com", Type: contract.SitePHP, PHPVersion: "8.5"},
	} {
		wantCode(t, Check(e.d, p), contract.ErrConflict)
	}
}

func TestPHPVersionChecks(t *testing.T) {
	e := newEnv(t)
	p := php84
	p.PHPVersion = "8.3" // not installed
	wantCode(t, Check(e.d, p), contract.ErrNotFound)

	writeFile(t, e.p.PHPFPMBinary("8.2"), "")
	p.PHPVersion = "8.2"
	if err := Check(e.d, p); err != nil {
		t.Errorf("8.2 before its end: %v", err)
	}
	e.d.Now = func() time.Time { return time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC) }
	ae := wantCode(t, Check(e.d, p), contract.ErrValidationFailed)
	if ae.Fields["php_version"] == "" {
		t.Errorf("fields = %v, want php_version", ae.Fields)
	}
}

func wantCode(t *testing.T, err error, code contract.ErrorCode) *contract.AgentError {
	t.Helper()
	var ae *contract.AgentError
	if !errors.As(err, &ae) || ae.Code != code {
		t.Fatalf("err = %v, want %s", err, code)
	}
	return ae
}

func checkMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	fi, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode() != want {
		t.Errorf("%s has mode %v, want %v", path, fi.Mode(), want)
	}
}
