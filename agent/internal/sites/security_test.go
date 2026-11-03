package sites

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"zimam/agent/contract"
	"zimam/agent/internal/audit"
)

const (
	hashAlice = "$2y$12$Mda0LhSrcwEX8fAIrkvr/eP5OR7zhBWHVUNmE9n4xm8B76JOYiAI."
	hashBob   = "$2y$12$ifXGrP1eih89gNPFeFYepuBiF7nCxjSFFfo5RtwlJTtWL9TDH4Hdq"
)

// securityEnv is the PHP site shop provisioned on 8.4, with FPM running.
// www-data's group is the test's own, so htpasswd can be given to it.
func securityEnv(t *testing.T) *env {
	t.Helper()
	e := newEnv(t)
	e.fpmActive("8.4", false, true)
	if _, err := e.provision(php84); err != nil {
		t.Fatalf("provisioning shop: %v", err)
	}
	b := strings.Replace(string(e.read(e.p.EtcGroup())), "www-data:x:33:", "www-data:x:"+strconv.Itoa(os.Getgid())+":", 1)
	writeFile(t, e.p.EtcGroup(), b)
	return e
}

// defaults is the settings a site has before anyone changes them, as the
// panel sends the whole set (§9.6).
func defaults() contract.SiteSecurityParams {
	return contract.SiteSecurityParams{
		Name:               "shop",
		HeadersPreset:      true,
		IPAllow:            contract.IPList{},
		IPDeny:             contract.IPList{},
		PasswordProtection: contract.PasswordProtection{Users: contract.BasicAuthUsers{}},
	}
}

func (e *env) secure(p contract.SiteSecurityParams) (contract.SiteSecurityResult, error) {
	return ApplySecurity(context.Background(), e.d, p)
}

// force_https needs an issued certificate (§9.6); a site that doesn't
// exist is not_found.
func TestForceHttpsWithoutCertificateConflict(t *testing.T) {
	e := securityEnv(t)
	p := defaults()
	p.ForceHTTPS = true
	wantCode(t, CheckSecurity(e.d, p), contract.ErrConflict)
	if _, err := e.secure(p); err == nil {
		t.Error("ApplySecurity forced HTTPS without a certificate")
	}

	writeCert(t, e.p.LetsEncryptCert("zimam-shop"))
	if err := CheckSecurity(e.d, p); err != nil {
		t.Errorf("with a certificate: %v", err)
	}
	other := defaults()
	other.Name = "blog"
	wantCode(t, CheckSecurity(e.d, other), contract.ErrNotFound)
}

// Templates render the parsed prefix (D21): a CIDR as written, a bare
// address as a /32 or /128; deny entries first, then allow and deny all.
func TestRendersCanonicalIPs(t *testing.T) {
	e := securityEnv(t)
	p := defaults()
	p.IPAllow = contract.IPList{"10.0.0.0/8", "192.0.2.7", "2001:db8::1"}
	p.IPDeny = contract.IPList{"10.9.0.0/16"}
	if _, err := e.secure(p); err != nil {
		t.Fatal(err)
	}
	conf := string(e.read(e.p.SiteSecurityConf("shop")))
	want := "deny 10.9.0.0/16;\nallow 10.0.0.0/8;\nallow 192.0.2.7/32;\nallow 2001:db8::1/128;\ndeny all;\n"
	if !strings.Contains(conf, want) {
		t.Errorf("security.conf =\n%s\nwant it to hold\n%s", conf, want)
	}
}

// One transaction holds security.conf, htpasswd, the vhost and the pool;
// its test is nginx -t then php-fpm -t. When PHP-FPM refuses, every file is
// back as it was, nothing reloads, and the state file keeps the old set.
func TestRollbackWhenPhpFpmTestFails(t *testing.T) {
	e := securityEnv(t)
	files := []string{e.p.SiteSecurityConf("shop"), e.p.NginxSiteAvailable("shop"), e.p.PHPPool("8.4", "shop")}
	before := map[string][]byte{}
	for _, f := range files {
		before[f] = e.read(f)
	}
	state := e.read(e.p.SiteState("shop"))
	p := defaults()
	p.HeadersPreset = false
	p.BlockRiskyFunctions = true
	p.IPDeny = contract.IPList{"203.0.113.0/24"}
	p.PasswordProtection = contract.PasswordProtection{Enabled: true, Users: contract.BasicAuthUsers{{Username: "alice", PasswordHash: hashAlice}}}
	e.failOnce[fpmTest84] = "ERROR: [pool zimam-shop] invalid value"
	n := len(e.run.Calls())

	_, err := e.secure(p)
	if err == nil || !strings.Contains(err.Error(), "invalid value") {
		t.Fatalf("ApplySecurity = %v, want PHP-FPM's test output", err)
	}
	for _, f := range files {
		if got := e.read(f); !bytes.Equal(got, before[f]) {
			t.Errorf("%s wasn't restored:\n%s", f, got)
		}
		if exists(f + ".zimam-prev") {
			t.Errorf("%s.zimam-prev was left behind", f)
		}
	}
	if exists(e.p.SiteHtpasswd("shop")) {
		t.Error("htpasswd is still there after the rollback")
	}
	if !bytes.Equal(e.read(e.p.SiteState("shop")), state) {
		t.Error("the state file changed")
	}
	got := e.callsSince(n)
	if want := []string{nginxTest, fpmTest84, nginxTest, fpmTest84}; !slices.Equal(got, want) {
		t.Errorf("ran\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// When Nginx refuses, PHP-FPM isn't even tested, and the files come back.
func TestRollbackWhenNginxTestFails(t *testing.T) {
	e := securityEnv(t)
	before := e.read(e.p.SiteSecurityConf("shop"))
	e.failOnce[nginxTest] = "nginx: [emerg] invalid"
	n := len(e.run.Calls())
	p := defaults()
	p.HeadersPreset = false
	if _, err := e.secure(p); err == nil {
		t.Fatal("ApplySecurity succeeded")
	}
	if !bytes.Equal(e.read(e.p.SiteSecurityConf("shop")), before) {
		t.Error("security.conf wasn't restored")
	}
	if got, want := e.callsSince(n), []string{nginxTest, nginxTest, fpmTest84}; !slices.Equal(got, want) {
		t.Errorf("ran %v, want %v", got, want)
	}
}

// On success both servers reload, and the state file holds the new set.
func TestSecurityAppliesAndStores(t *testing.T) {
	e := securityEnv(t)
	writeCert(t, e.p.LetsEncryptCert("zimam-shop"))
	p := defaults()
	p.ForceHTTPS = true
	age := contract.HSTSMaxAge(31536000)
	p.HSTS = contract.HSTS{MaxAge: &age, IncludeSubdomains: true}
	p.RestrictToSiteFolder = true
	p.IPAllow = contract.IPList{"192.0.2.0/24"}
	n := len(e.run.Calls())
	res, err := e.secure(p)
	if err != nil {
		t.Fatal(err)
	}
	if res != (contract.SiteSecurityResult{Name: "shop"}) {
		t.Errorf("result = %+v", res)
	}
	got := e.callsSince(n)
	if want := []string{nginxTest, fpmTest84, nginxReload, fpmTest84, fpmActive84, fpmReload84}; !slices.Equal(got, want) {
		t.Errorf("ran\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	st, err := Load(e.p, "shop")
	if err != nil {
		t.Fatal(err)
	}
	want := Security{ForceHTTPS: true, HSTSMaxAge: 31536000, HSTSIncludeSubdomains: true, HeadersPreset: true, RestrictToSiteFolder: true, IPAllow: contract.IPList{"192.0.2.0/24"}, IPDeny: contract.IPList{}}
	if !securityEqual(st.Security, want) {
		t.Errorf("state = %+v, want %+v", st.Security, want)
	}
	if conf := string(e.read(e.p.SiteSecurityConf("shop"))); !strings.Contains(conf, `max-age=31536000; includeSubDomains`) {
		t.Errorf("security.conf has no HSTS header:\n%s", conf)
	}
	if pool := string(e.read(e.p.PHPPool("8.4", "shop"))); !strings.Contains(pool, "open_basedir") || strings.Contains(pool, "disable_functions") {
		t.Errorf("pool =\n%s", pool)
	}
	if vhost := string(e.read(e.p.NginxSiteAvailable("shop"))); !strings.Contains(vhost, "return 301 https://") {
		t.Errorf("the vhost doesn't redirect:\n%s", vhost)
	}

	// Sending the same set again changes nothing and reloads nothing.
	n = len(e.run.Calls())
	if _, err := e.secure(p); err != nil {
		t.Fatal(err)
	}
	if got := e.callsSince(n); len(got) != 0 {
		t.Errorf("the same settings again ran %v", got)
	}

	// site.php and cert.issue re-render the vhost through ensureVhost, which
	// keeps the settings in the state file.
	writeFile(t, e.p.SiteSecurityConf("shop"), "# stale\n")
	if err := ensureVhost(context.Background(), e.d, st); err != nil {
		t.Fatal(err)
	}
	if conf := string(e.read(e.p.SiteSecurityConf("shop"))); !strings.Contains(conf, "allow 192.0.2.0/24;") || !strings.Contains(conf, "max-age=31536000") {
		t.Errorf("ensureVhost dropped the settings:\n%s", conf)
	}
}

// A static site has no pool, so only Nginx is tested and reloaded.
func TestSecurityStaticSite(t *testing.T) {
	e := newEnv(t)
	if _, err := e.provision(static); err != nil {
		t.Fatal(err)
	}
	n := len(e.run.Calls())
	p := defaults()
	p.HeadersPreset = false
	if _, err := e.secure(p); err != nil {
		t.Fatal(err)
	}
	if got, want := e.callsSince(n), []string{nginxTest, nginxReload}; !slices.Equal(got, want) {
		t.Errorf("ran %v, want %v", got, want)
	}
}

// htpasswd is root:www-data 0640 (§5.9), so Nginx's workers read it and site
// users don't; turning password protection off removes it.
func TestHtpasswdModeAndOwner(t *testing.T) {
	e := securityEnv(t)
	p := defaults()
	p.PasswordProtection = contract.PasswordProtection{Enabled: true, Users: contract.BasicAuthUsers{{Username: "alice", PasswordHash: hashAlice}, {Username: "bob", PasswordHash: hashBob}}}
	if _, err := e.secure(p); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(e.p.SiteHtpasswd("shop"))
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o640 {
		t.Errorf("htpasswd mode %o, want 0640", fi.Mode().Perm())
	}
	if gid := fi.Sys().(*syscall.Stat_t).Gid; int(gid) != os.Getgid() {
		t.Errorf("htpasswd group %d, want www-data's (%d)", gid, os.Getgid())
	}
	if got := string(e.read(e.p.SiteHtpasswd("shop"))); !strings.HasSuffix(got, "alice:"+hashAlice+"\nbob:"+hashBob+"\n") {
		t.Errorf("htpasswd =\n%s", got)
	}
	if conf := string(e.read(e.p.SiteSecurityConf("shop"))); !strings.Contains(conf, "auth_basic_user_file /etc/nginx/zimam/shop/htpasswd;") {
		t.Errorf("security.conf =\n%s", conf)
	}

	if _, err := e.secure(defaults()); err != nil {
		t.Fatal(err)
	}
	if exists(e.p.SiteHtpasswd("shop")) {
		t.Error("htpasswd is still there with password protection off")
	}
}

// The hashes are secret: the audit log gets them redacted (§5.8), and
// they never reach a command line or a command's environment (§13).
func TestHashesRedactedInAudit(t *testing.T) {
	p := defaults()
	p.PasswordProtection = contract.PasswordProtection{Enabled: true, Users: contract.BasicAuthUsers{{Username: "alice", PasswordHash: hashAlice}}}
	b, err := json.Marshal(audit.Redact(&p))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), hashAlice) || !strings.Contains(string(b), `"password_hash":"[redacted]"`) || !strings.Contains(string(b), `"username":"alice"`) {
		t.Errorf("redacted params = %s", b)
	}
}

func TestNoSecretInArgv(t *testing.T) {
	e := securityEnv(t)
	p := defaults()
	p.PasswordProtection = contract.PasswordProtection{Enabled: true, Users: contract.BasicAuthUsers{{Username: "alice", PasswordHash: hashAlice}}}
	if _, err := e.secure(p); err != nil {
		t.Fatal(err)
	}
	secret := strings.TrimPrefix(hashAlice, "$2y$12$")
	for _, c := range e.run.Calls() {
		all := strings.Join(append(append([]string{c.Path}, c.Args...), c.Env...), " ") + string(c.Stdin)
		if strings.Contains(all, secret) {
			t.Errorf("a command carried the hash: %s", all)
		}
	}
}

func securityEqual(a, b Security) bool {
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	return bytes.Equal(ja, jb)
}
