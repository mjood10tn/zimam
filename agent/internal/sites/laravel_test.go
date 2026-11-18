package sites

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"io/fs"
	"os"
	"slices"
	"strings"
	"testing"

	"zimam/agent/contract"
	"zimam/agent/internal/templates"
)

var laravel84 = contract.SiteProvisionParams{Name: "shop", Domain: "shop.example.com", Type: contract.SiteLaravel, PHPVersion: "8.4"}

const sharedEnv = "site/shared/.env"

// laravelDirs are the shared directories a laravel site gets (R02-D8).
// www-data, in the site's group, reaches storage/app/public through
// public/storage, so that chain is 0750; the rest holds sessions, cache and
// logs, which only the site user reads, so it is 0700.
var laravelDirs = []struct {
	name string
	mode fs.FileMode
}{
	{"site/shared", 0o750},
	{"site/shared/storage", 0o750},
	{"site/shared/storage/app", 0o750},
	{"site/shared/storage/app/public", 0o750},
	{"site/shared/storage/framework", 0o700},
	{"site/shared/storage/framework/cache", 0o700},
	{"site/shared/storage/framework/sessions", 0o700},
	{"site/shared/storage/framework/views", 0o700},
	{"site/shared/storage/logs", 0o700},
}

// provisionLaravel provisions laravel84 with FPM 8.4 not yet running, and
// captures what the job log got.
func (e *env) provisionLaravel() (contract.SiteProvisionResult, *bytes.Buffer, error) {
	e.t.Helper()
	e.fpmActive("8.4", false, true)
	var log bytes.Buffer
	e.d.Logf = func(format string, args ...any) { fmt.Fprintf(&log, format+"\n", args...) }
	res, err := e.provision(laravel84)
	return res, &log, err
}

// appKey is the APP_KEY line of the site's shared .env.
func (e *env) appKey() string {
	e.t.Helper()
	n := e.help.nodes[sharedEnv]
	if n == nil {
		e.t.Fatal("no shared .env")
	}
	for line := range strings.Lines(string(n.content)) {
		if k, ok := strings.CutPrefix(line, "APP_KEY="); ok {
			return strings.TrimSuffix(k, "\n")
		}
	}
	e.t.Fatalf("the .env has no APP_KEY:\n%s", n.content)
	return ""
}

// A laravel site is a PHP site, with its pool and the PHP vhost, that also
// gets the shared directories, its .env and log rules for its storage
// (R02-D8), at template version 2.
func TestProvisionLaravelCreatesShared(t *testing.T) {
	e := newEnv(t)
	res, _, err := e.provisionLaravel()
	if err != nil {
		t.Fatalf("Provision: %v", err)
	}
	if res != (contract.SiteProvisionResult{Name: "shop", TemplateVersion: templates.Version}) {
		t.Errorf("result = %+v", res)
	}
	want := []string{groupaddCmd, useraddCmd, usermodCmd, fpmTest84, fpmTest84, fpmActive84, fpmEnable84, nginxTest, nginxReload, e.logrotateTest()}
	if got := e.calls(); !slices.Equal(got, want) {
		t.Errorf("ran\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if pool, err := os.ReadFile(e.p.PHPPool("8.4", "shop")); err != nil || !strings.Contains(string(pool), "listen = /run/php/zimam-shop-8.4.sock") {
		t.Errorf("pool = %q, %v", pool, err)
	}
	vhost, _ := os.ReadFile(e.p.NginxSiteAvailable("shop"))
	if !strings.Contains(string(vhost), "fastcgi_pass unix:/run/php/zimam-shop-8.4.sock;") || !strings.Contains(string(vhost), "root /home/s_shop/site/current/public;") {
		t.Errorf("the vhost isn't the PHP one:\n%s", vhost)
	}
	if e.help.nodes[placeholder+"/index.php"] == nil {
		t.Error("the placeholder isn't the PHP one")
	}
	for _, dir := range laravelDirs {
		n := e.help.nodes[dir.name]
		if n == nil || !n.dir || n.mode != dir.mode {
			t.Errorf("%s = %+v, want a %o directory", dir.name, n, dir.mode)
		}
		if op := fmt.Sprintf("%s mkdir %s %o", helperUser, dir.name, dir.mode); !slices.Contains(e.help.changes(), op) {
			t.Errorf("%s wasn't made %o explicitly: %q", dir.name, dir.mode, e.help.changes())
		}
	}
	env := e.help.nodes[sharedEnv]
	if env == nil || env.dir || env.mode != 0o600 {
		t.Fatalf(".env = %+v, want a 0600 file", env)
	}
	for _, line := range []string{"APP_ENV=production\n", "APP_DEBUG=false\n", "APP_URL=https://shop.example.com\n", "LOG_CHANNEL=single\n"} {
		if !strings.Contains(string(env.content), line) {
			t.Errorf(".env lacks %q:\n%s", line, env.content)
		}
	}
	rules, err := os.ReadFile(e.p.SiteLogrotateConf("shop"))
	if err != nil || !strings.Contains(string(rules), "/home/s_shop/site/shared/storage/logs/*.log {\n    su s_shop s_shop\n") {
		t.Errorf("log rules = %q, %v", rules, err)
	}
	st, err := Load(e.p, "shop")
	if err != nil || st.Type != contract.SiteLaravel || st.PHPVersion != "8.4" || st.TemplateVersion != templates.Version {
		t.Errorf("state = %+v, %v", st, err)
	}
	if err := Check(e.d, laravel84); err != nil {
		t.Errorf("Check of a retry: %v", err)
	}
}

// APP_KEY is 32 bytes from crypto/rand, as Laravel's key:generate makes
// it, different per site. It reaches the .env only through the helper's
// stdin: never a command's argv, the job log, the state file or the result.
func TestLaravelEnvHasGeneratedAppKey(t *testing.T) {
	e := newEnv(t)
	res, log, err := e.provisionLaravel()
	if err != nil {
		t.Fatal(err)
	}
	key := e.appKey()
	raw, ok := strings.CutPrefix(key, "base64:")
	if !ok {
		t.Fatalf("APP_KEY %q has no base64: prefix", key)
	}
	if b, err := base64.StdEncoding.Strict().DecodeString(raw); err != nil || len(b) != 32 {
		t.Errorf("APP_KEY decodes to %d bytes, %v; want 32", len(b), err)
	} else if bytes.Equal(b, make([]byte, 32)) {
		t.Error("APP_KEY is all zeros")
	}
	for _, c := range e.calls() {
		if strings.Contains(c, raw) {
			t.Errorf("the key is in argv: %s", c)
		}
	}
	for _, op := range e.help.ops {
		if strings.Contains(op, raw) {
			t.Errorf("the key is in a helper op's arguments: %s", op)
		}
	}
	if strings.Contains(log.String(), raw) {
		t.Errorf("the key is in the job log:\n%s", log)
	}
	if st, _ := os.ReadFile(e.p.SiteState("shop")); bytes.Contains(st, []byte(raw)) {
		t.Error("the key is in the state file")
	}
	if strings.Contains(fmt.Sprintf("%+v", res), raw) {
		t.Error("the key is in the result")
	}

	other := newEnv(t)
	if _, _, err := other.provisionLaravel(); err != nil {
		t.Fatal(err)
	}
	if other.appKey() == key {
		t.Error("two sites got the same APP_KEY")
	}
}

// The .env is the site user's, written through the helper: root writes
// nothing under the home. A retry never writes it again, so the site keeps
// its APP_KEY and whatever the owner changed.
func TestLaravelEnvWrittenThroughHelper(t *testing.T) {
	e := newEnv(t)
	if _, _, err := e.provisionLaravel(); err != nil {
		t.Fatal(err)
	}
	if entries, err := os.ReadDir(e.p.SiteHome("shop")); err != nil || len(entries) != 0 {
		t.Errorf("root wrote %v under the home (%v)", entries, err)
	}
	if !slices.Contains(e.help.changes(), helperUser+" write "+sharedEnv+" 600") {
		t.Errorf("the .env wasn't written through the helper as the site user: %q", e.help.changes())
	}
	if i, j := slices.Index(e.help.changes(), helperUser+" mkdir site/shared 750"), slices.Index(e.help.changes(), helperUser+" write "+sharedEnv+" 600"); i < 0 || j < i {
		t.Errorf("the .env was written before site/shared was made: %q", e.help.changes())
	}

	e.help.nodes[sharedEnv].content = []byte("APP_KEY=the owner's\n")
	ops := len(e.help.changes())
	if _, err := e.provision(laravel84); err != nil {
		t.Fatalf("the retry: %v", err)
	}
	if got := e.help.changes()[ops:]; len(got) != 0 {
		t.Errorf("the retry changed the home again: %q", got)
	}
	if got := string(e.help.nodes[sharedEnv].content); got != "APP_KEY=the owner's\n" {
		t.Errorf("the retry rewrote the .env: %q", got)
	}
}

// A provision that stopped before the .env, or with only part of the
// shared tree, finishes it on the retry.
func TestLaravelRetryFinishesShared(t *testing.T) {
	e := newEnv(t)
	if _, _, err := e.provisionLaravel(); err != nil {
		t.Fatal(err)
	}
	delete(e.help.nodes, sharedEnv)
	delete(e.help.nodes, "site/shared/storage/framework/views")
	ops := len(e.help.changes())
	if _, err := e.provision(laravel84); err != nil {
		t.Fatal(err)
	}
	got := e.help.changes()[ops:]
	want := []string{helperUser + " mkdir site/shared/storage/framework/views 700", helperUser + " write " + sharedEnv + " 600"}
	if !slices.Equal(got, want) {
		t.Errorf("the retry did %q, want %q", got, want)
	}
}

// A laravel site behaves as a PHP site everywhere a site's PHP matters:
// site.security re-renders its pool, site.php switches it, and site.destroy
// stops the FPM its state file names even when the pool is already gone.
func TestLaravelSiteRunsPHP(t *testing.T) {
	t.Run("security", func(t *testing.T) {
		e := newEnv(t)
		if _, _, err := e.provisionLaravel(); err != nil {
			t.Fatal(err)
		}
		p := defaults()
		p.BlockRiskyFunctions = true
		if _, err := e.secure(p); err != nil {
			t.Fatal(err)
		}
		if pool := string(e.read(e.p.PHPPool("8.4", "shop"))); !strings.Contains(pool, "disable_functions") {
			t.Errorf("the pool wasn't re-rendered:\n%s", pool)
		}
	})
	t.Run("switch", func(t *testing.T) {
		e := newEnv(t)
		if _, _, err := e.provisionLaravel(); err != nil {
			t.Fatal(err)
		}
		e.fpmActive("8.5", false)
		old := drainWait
		t.Cleanup(func() { drainWait = old })
		drainWait = 0
		if err := CheckSwitch(e.d, to85); err != nil {
			t.Fatalf("CheckSwitch: %v", err)
		}
		if _, err := e.switchTo(to85); err != nil {
			t.Fatalf("Switch: %v", err)
		}
		st, err := Load(e.p, "shop")
		if err != nil || st.PHPVersion != "8.5" || st.Type != contract.SiteLaravel {
			t.Errorf("state = %+v, %v", st, err)
		}
		if !exists(e.p.PHPPool("8.5", "shop")) || exists(e.p.PHPPool("8.4", "shop")) {
			t.Error("the pool didn't move to 8.5")
		}
	})
	t.Run("destroy", func(t *testing.T) {
		e := newEnv(t)
		if _, _, err := e.provisionLaravel(); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(e.p.PHPPool("8.4", "shop")); err != nil {
			t.Fatal(err)
		}
		n := len(e.run.Calls())
		if _, err := e.destroy(); err != nil {
			t.Fatal(err)
		}
		if got := e.callsSince(n); !slices.Contains(got, fpmDisable84) {
			t.Errorf("ran %q, want FPM 8.4 stopped", got)
		}
	})
	t.Run("state", func(t *testing.T) {
		e := newEnv(t)
		err := Save(e.p, State{Name: "shop", Domain: "shop.example.com", Type: contract.SiteLaravel, TemplateVersion: 2, Security: DefaultSecurity()})
		if err == nil {
			t.Error("saved a laravel site without a PHP version")
		}
	})
}
