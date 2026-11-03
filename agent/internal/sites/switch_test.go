package sites

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"zimam/agent/contract"
)

const (
	fpmTest85    = "/usr/sbin/php-fpm8.5 -t"
	fpmActive85  = "/usr/bin/systemctl is-active --quiet php8.5-fpm"
	fpmEnable85  = "/usr/bin/systemctl enable --now php8.5-fpm"
	fpmDisable84 = "/usr/bin/systemctl disable --now php8.4-fpm"
	fpmDisable85 = "/usr/bin/systemctl disable --now php8.5-fpm"
)

var to85 = contract.SitePHPParams{Name: "shop", PHPVersion: "8.5"}

// switchEnv is a server with the PHP site shop provisioned on 8.4, PHP 8.5
// installed and not running, and no wait before the old pool goes.
func switchEnv(t *testing.T) *env {
	t.Helper()
	e := newEnv(t)
	e.fpmActive("8.4", false)
	if _, err := e.provision(php84); err != nil {
		t.Fatalf("provisioning shop: %v", err)
	}
	e.fpmActive("8.5", false)
	old := drainWait
	t.Cleanup(func() { drainWait = old })
	drainWait = 0
	return e
}

func (e *env) switchTo(p contract.SitePHPParams) (contract.SitePHPResult, error) {
	return Switch(context.Background(), e.d, p)
}

func (e *env) read(path string) []byte {
	e.t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		e.t.Fatal(err)
	}
	return b
}

// The new pool serves before Nginx points at it, and the old pool goes only
// after Nginx has moved off it (§9.5), so no request finds no pool.
func TestSwitchOrder(t *testing.T) {
	e := switchEnv(t)
	n := len(e.run.Calls())
	res, err := e.switchTo(to85)
	if err != nil {
		t.Fatalf("Switch: %v", err)
	}
	if res != (contract.SitePHPResult{Name: "shop", PHPVersion: "8.5"}) {
		t.Errorf("result = %+v", res)
	}
	got := e.callsSince(n)
	want := []string{fpmTest85, fpmTest85, fpmActive85, fpmEnable85, nginxTest, nginxReload, fpmDisable84}
	if !slices.Equal(got, want) {
		t.Errorf("ran\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if exists(e.p.PHPPool("8.4", "shop")) {
		t.Error("the old pool is still there")
	}
	if !exists(e.p.PHPPool("8.5", "shop")) {
		t.Error("the new pool wasn't written")
	}
	if vhost := e.read(e.p.NginxSiteAvailable("shop")); !bytes.Contains(vhost, []byte("fastcgi_pass unix:/run/php/zimam-shop-8.5.sock;")) {
		t.Errorf("the vhost doesn't pass PHP to the new pool:\n%s", vhost)
	}
	st, err := Load(e.p, "shop")
	if err != nil || st.PHPVersion != "8.5" {
		t.Errorf("state = %+v, %v; want PHP 8.5", st, err)
	}
}

// FPM reloads in the background, so Nginx waits for the new pool's socket.
// If it never comes, the switch fails before Nginx is touched.
func TestSwitchWaitsForSocket(t *testing.T) {
	defer func(w time.Duration) { socketWait = w }(socketWait)
	socketWait = 300 * time.Millisecond
	e := switchEnv(t)
	vhost := e.read(e.p.NginxSiteAvailable("shop"))
	e.noSockets = true
	n := len(e.run.Calls())
	_, err := e.switchTo(to85)
	if err == nil || !strings.Contains(err.Error(), "zimam-shop-8.5.sock") {
		t.Fatalf("Switch = %v, want the missing socket named", err)
	}
	if got := e.callsSince(n); slices.Contains(got, nginxReload) {
		t.Errorf("Nginx was reloaded without the new socket: %q", got)
	}
	if !bytes.Equal(e.read(e.p.NginxSiteAvailable("shop")), vhost) {
		t.Error("the vhost changed")
	}
}

// A switch that fails at step 1, whether php-fpm -t refuses the new pool or
// FPM never makes its socket, removes the new pool and leaves the site as
// it was: its old pool, vhost and state file.
func TestSwitchStep1FailureLeavesSiteUntouched(t *testing.T) {
	defer func(w time.Duration) { socketWait = w }(socketWait)
	socketWait = 300 * time.Millisecond
	for name, fail := range map[string]func(e *env){
		"php-fpm -t": func(e *env) { e.failOnce[fpmTest85] = "ERROR: [pool zimam-shop] bogus" },
		"no socket":  func(e *env) { e.noSockets = true },
	} {
		t.Run(name, func(t *testing.T) {
			e := switchEnv(t)
			vhost, oldPool, state := e.read(e.p.NginxSiteAvailable("shop")), e.read(e.p.PHPPool("8.4", "shop")), e.read(e.p.SiteState("shop"))
			fail(e)
			n := len(e.run.Calls())
			if _, err := e.switchTo(to85); err == nil {
				t.Fatal("Switch passed")
			}
			got := e.callsSince(n)
			if exists(e.p.PHPPool("8.5", "shop")) {
				t.Error("the new pool is left behind")
			}
			if !slices.Contains(got, fpmDisable85) {
				t.Errorf("ran %q, want PHP 8.5's FPM stopped again, with no pool left", got)
			}
			if slices.Contains(got, nginxTest) || slices.Contains(got, fpmDisable84) {
				t.Errorf("ran %q after step 1 failed", got)
			}
			if !bytes.Equal(e.read(e.p.NginxSiteAvailable("shop")), vhost) || !bytes.Equal(e.read(e.p.PHPPool("8.4", "shop")), oldPool) || !bytes.Equal(e.read(e.p.SiteState("shop")), state) {
				t.Error("the site's vhost, old pool or state file changed")
			}
		})
	}
}

// When Nginx refuses the new vhost, the old one is back, the old pool still
// runs, and the new pool is gone.
func TestSwitchStep2FailureRestoresVhostAndRemovesNewPool(t *testing.T) {
	e := switchEnv(t)
	vhost, state := e.read(e.p.NginxSiteAvailable("shop")), e.read(e.p.SiteState("shop"))
	failNginx(e)
	n := len(e.run.Calls())
	_, err := e.switchTo(to85)
	if err == nil || !strings.Contains(err.Error(), "unknown directive") {
		t.Fatalf("Switch = %v, want nginx -t's output", err)
	}
	got := e.callsSince(n)
	if !bytes.Equal(e.read(e.p.NginxSiteAvailable("shop")), vhost) {
		t.Error("the old vhost wasn't restored")
	}
	if !exists(e.p.PHPPool("8.4", "shop")) || slices.Contains(got, fpmDisable84) {
		t.Errorf("the old pool was touched: ran %q", got)
	}
	if exists(e.p.PHPPool("8.5", "shop")) || !slices.Contains(got, fpmDisable85) {
		t.Errorf("the new pool is left running: ran %q", got)
	}
	if !bytes.Equal(e.read(e.p.SiteState("shop")), state) {
		t.Error("the state file changed")
	}
}

// The old version's FPM is stopped and disabled when the site's pool was its
// last, without php-fpm -t, which refuses a config with no pool. While
// another pool is left, it is tested and reloaded instead.
func TestSwitchStopsOldFPMWhenLastPool(t *testing.T) {
	e := switchEnv(t)
	n := len(e.run.Calls())
	if _, err := e.switchTo(to85); err != nil {
		t.Fatal(err)
	}
	if got := e.callsSince(n); !slices.Contains(got, fpmDisable84) || slices.Contains(got, fpmTest84) || slices.Contains(got, fpmReload84) {
		t.Errorf("ran %q, want PHP 8.4's FPM stopped, untested", got)
	}

	e = switchEnv(t)
	writeFile(t, e.p.PHPPool("8.4", "blog"), "[zimam-blog]\n")
	e.fpmActive("8.4", true)
	n = len(e.run.Calls())
	if _, err := e.switchTo(to85); err != nil {
		t.Fatal(err)
	}
	if got := e.callsSince(n); slices.Contains(got, fpmDisable84) || !slices.Contains(got, fpmTest84) || !slices.Contains(got, fpmReload84) {
		t.Errorf("ran %q, want PHP 8.4's FPM tested and reloaded for blog", got)
	}
}

// A switch whose step 3 failed is finished by the same switch again: steps 1
// and 2 find their work done, and the old pool goes.
func TestSwitchRetryFinishes(t *testing.T) {
	e := switchEnv(t)
	e.failOnce[fpmDisable84] = "Failed to stop php8.4-fpm.service"
	if _, err := e.switchTo(to85); err == nil {
		t.Fatal("Switch passed with the old FPM failing to stop")
	}
	if st, _ := Load(e.p, "shop"); st.PHPVersion != "8.4" {
		t.Errorf("state says PHP %s after a failed step 3, want 8.4", st.PHPVersion)
	}
	e.fpmActive("8.5", true)
	n := len(e.run.Calls())
	if _, err := e.switchTo(to85); err != nil {
		t.Fatalf("the retry: %v", err)
	}
	want := []string{fpmActive85, fpmDisable84}
	if got := e.callsSince(n); !slices.Equal(got, want) {
		t.Errorf("the retry ran\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if st, _ := Load(e.p, "shop"); st.PHPVersion != "8.5" {
		t.Errorf("state says PHP %s, want 8.5", st.PHPVersion)
	}
}

// A site with a certificate keeps its HTTPS server and redirect.
func TestSwitchKeepsHTTPS(t *testing.T) {
	e := switchEnv(t)
	st, _ := Load(e.p, "shop")
	st.Security.ForceHTTPS = true
	if err := Save(e.p, st); err != nil {
		t.Fatal(err)
	}
	mkdir(t, filepath.Dir(e.p.LetsEncryptCert("zimam-shop")))
	writeFile(t, e.p.LetsEncryptCert("zimam-shop"), "")
	if _, err := e.switchTo(to85); err != nil {
		t.Fatal(err)
	}
	vhost := string(e.read(e.p.NginxSiteAvailable("shop")))
	if !strings.Contains(vhost, "listen 443 ssl;") || !strings.Contains(vhost, "return 301 https://") {
		t.Errorf("the vhost lost HTTPS:\n%s", vhost)
	}
}

func TestSwitchStaticConflict(t *testing.T) {
	e := newEnv(t)
	if _, err := e.provision(static); err != nil {
		t.Fatal(err)
	}
	wantCode(t, CheckSwitch(e.d, to85), contract.ErrConflict)
}

func TestSwitchSameVersionConflict(t *testing.T) {
	e := switchEnv(t)
	wantCode(t, CheckSwitch(e.d, contract.SitePHPParams{Name: "shop", PHPVersion: "8.4"}), contract.ErrConflict)
	if err := CheckSwitch(e.d, to85); err != nil {
		t.Errorf("8.4 to 8.5: %v", err)
	}
}

func TestSwitchChecks(t *testing.T) {
	e := switchEnv(t)
	wantCode(t, CheckSwitch(e.d, contract.SitePHPParams{Name: "blog", PHPVersion: "8.5"}), contract.ErrNotFound)
	wantCode(t, CheckSwitch(e.d, contract.SitePHPParams{Name: "shop", PHPVersion: "8.3"}), contract.ErrNotFound)

	writeFile(t, e.p.PHPFPMBinary("8.2"), "")
	e.d.Now = func() time.Time { return time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC) }
	ae := wantCode(t, CheckSwitch(e.d, contract.SitePHPParams{Name: "shop", PHPVersion: "8.2"}), contract.ErrValidationFailed)
	if ae.Fields["php_version"] == "" {
		t.Errorf("fields = %v, want php_version", ae.Fields)
	}
}
