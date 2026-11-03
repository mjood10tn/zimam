package templates

import (
	"bytes"
	"flag"
	"fmt"
	"io/fs"
	"net/netip"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"zimam/agent/contract"
	"zimam/agent/internal/paths"
)

var update = flag.Bool("update", false, "rewrite testdata/golden from the templates")

const goldenDir = "testdata/golden"

// goldenFile is one rendered file of a golden case, at its path on a server.
type goldenFile struct {
	path string
	tmpl string
	data func() any // nil for a template that takes no data
}

func (f goldenFile) render() ([]byte, error) {
	var data any
	if f.data != nil {
		data = f.data()
	}
	return Render(f.tmpl, data)
}

// goldenCases are the file trees configcheck installs and tests, each in a
// fresh container (design §14.1). A case must work on its own, so a vhost
// brings the snippets it includes.
var goldenCases = map[string][]goldenFile{
	// What bootstrap writes for Nginx (§4 step 5).
	"nginx-base": {
		{path: "/etc/nginx/zimam/tls.conf", tmpl: "nginx/tls.conf"},
		{path: "/etc/nginx/sites-available/zimam-default.conf", tmpl: "nginx/default.conf"},
	},
	// The sury repository (§8.2), from the default php_repo_url and from a
	// mirror.
	"apt-sury":        suryRepo("https://packages.sury.org/php/", "packages.sury.org"),
	"apt-sury-mirror": suryRepo("https://mirror.example.com/sury/php/", "mirror.example.com"),
	// What php.install writes for each version on the allowlist (§8.3).
	"php-base": phpBase(),
	// The panel's vhost and pool (§7.1, §9.3), before and after its
	// certificate exists.
	"panel-http":  panel(false),
	"panel-https": panel(true),
	// Zimam's own units (§7.1, §11.3).
	"units": units(),
	// The log policy (§11.3): Zimam's own rules, one site's, and the journal's.
	"log-policy": logPolicy(),
	// A site's vhost with its default security.conf, static or PHP, before
	// and after its certificate exists; the PHP cases carry their pool, one
	// on 8.4 and one on 8.5 (§9.3, §9.4, §9.6).
	"site-static-http":  site(SiteVhost{Name: "shop", Domain: "shop.example.com"}),
	"site-php-http":     site(SiteVhost{Name: "shop", Domain: "shop.example.com", PHPVersion: "8.4"}),
	"site-static-https": site(SiteVhost{Name: "shop", Domain: "shop.example.com", HTTPS: true, ForceHTTPS: true}),
	"site-php-https":    site(SiteVhost{Name: "blog", Domain: "blog.example.com", PHPVersion: "8.5", HTTPS: true}),
	// One case per row of §9.6, each on a PHP site over HTTPS with the
	// redirect: every HSTS max-age with and without include_subdomains,
	// the header preset off, an allow list, a deny list and both, password
	// protection, and each pool setting alone and together.
	"security-hsts-86400":                 secured(SiteSecurity{HeadersPreset: true, HSTSMaxAge: 86400}, SitePool{}),
	"security-hsts-86400-subdomains":      secured(SiteSecurity{HeadersPreset: true, HSTSMaxAge: 86400, HSTSIncludeSubdomains: true}, SitePool{}),
	"security-hsts-2592000":               secured(SiteSecurity{HeadersPreset: true, HSTSMaxAge: 2592000}, SitePool{}),
	"security-hsts-2592000-subdomains":    secured(SiteSecurity{HeadersPreset: true, HSTSMaxAge: 2592000, HSTSIncludeSubdomains: true}, SitePool{}),
	"security-hsts-31536000":              secured(SiteSecurity{HeadersPreset: true, HSTSMaxAge: 31536000}, SitePool{}),
	"security-hsts-31536000-subdomains":   secured(SiteSecurity{HeadersPreset: true, HSTSMaxAge: 31536000, HSTSIncludeSubdomains: true}, SitePool{}),
	"security-headers-off":                secured(SiteSecurity{}, SitePool{}),
	"security-ip-allow":                   secured(SiteSecurity{HeadersPreset: true, IPAllow: testAllow}, SitePool{}),
	"security-ip-deny":                    secured(SiteSecurity{HeadersPreset: true, IPDeny: testDeny}, SitePool{}),
	"security-ip-both":                    secured(SiteSecurity{HeadersPreset: true, IPAllow: testAllow, IPDeny: testDeny}, SitePool{}),
	"security-password":                   secured(SiteSecurity{HeadersPreset: true, PasswordProtection: true}, SitePool{}),
	"security-block-risky-functions":      secured(SiteSecurity{HeadersPreset: true}, SitePool{BlockRiskyFunctions: true}),
	"security-restrict-to-site-folder":    secured(SiteSecurity{HeadersPreset: true}, SitePool{RestrictToSiteFolder: true}),
	"security-risky-functions-and-folder": secured(SiteSecurity{HeadersPreset: true}, SitePool{BlockRiskyFunctions: true, RestrictToSiteFolder: true}),
}

// testAllow and testDeny are parsed entries, as contract.ParseIPEntry
// returns them: a bare address is a /32 or /128 prefix (D21).
var (
	testAllow = []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8"), netip.MustParsePrefix("192.0.2.7/32"), netip.MustParsePrefix("2001:db8::/32")}
	testDeny  = []netip.Prefix{netip.MustParsePrefix("10.1.0.0/16"), netip.MustParsePrefix("2001:db8:dead::1/128")}
	testUsers = contract.BasicAuthUsers{
		{Username: "alice", PasswordHash: "$2y$12$Mda0LhSrcwEX8fAIrkvr/eP5OR7zhBWHVUNmE9n4xm8B76JOYiAI."},
		{Username: "bob.smith", PasswordHash: "$2y$12$ifXGrP1eih89gNPFeFYepuBiF7nCxjSFFfo5RtwlJTtWL9TDH4Hdq"},
	}
)

// secured is the PHP site shop over HTTPS with force_https, with these
// security settings and pool flags; password protection brings htpasswd.
func secured(sec SiteSecurity, pool SitePool) []goldenFile {
	var p paths.Paths
	sec.Name = "shop"
	pool.Name, pool.PHPVersion = "shop", "8.4"
	files := []goldenFile{
		{path: p.NginxSiteAvailable("shop"), tmpl: "nginx/site.conf", data: func() any {
			return SiteVhost{Name: "shop", Domain: "shop.example.com", PHPVersion: "8.4", HTTPS: true, ForceHTTPS: true}
		}},
		{path: p.SiteSecurityConf("shop"), tmpl: "nginx/security.conf", data: func() any { return sec }},
		{path: "/etc/nginx/zimam/tls.conf", tmpl: "nginx/tls.conf"},
		{path: p.PHPPool("8.4", "shop"), tmpl: "php/site-pool.conf", data: func() any { return pool }},
	}
	if sec.PasswordProtection {
		files = append(files, goldenFile{path: p.SiteHtpasswd("shop"), tmpl: "nginx/htpasswd", data: func() any { return Htpasswd{Users: testUsers} }})
	}
	return files
}

func site(v SiteVhost) []goldenFile {
	var p paths.Paths
	files := []goldenFile{
		{path: p.NginxSiteAvailable(v.Name), tmpl: "nginx/site.conf", data: func() any { return v }},
		{path: p.SiteSecurityConf(v.Name), tmpl: "nginx/security.conf", data: func() any { return SiteSecurity{HeadersPreset: true} }},
	}
	if v.HTTPS {
		files = append(files, goldenFile{path: "/etc/nginx/zimam/tls.conf", tmpl: "nginx/tls.conf"})
	}
	if v.PHPVersion != "" {
		files = append(files, goldenFile{path: p.PHPPool(v.PHPVersion, v.Name), tmpl: "php/site-pool.conf", data: func() any {
			return SitePool{Name: v.Name, PHPVersion: v.PHPVersion}
		}})
	}
	return files
}

func panel(https bool) []goldenFile {
	var p paths.Paths
	files := []goldenFile{
		{path: p.NginxPanelAvailable(), tmpl: "nginx/panel.conf", data: func() any { return PanelVhost{Domain: "panel.example.com", HTTPS: https} }},
		{path: p.PanelPool("8.4"), tmpl: "php/panel-pool.conf"},
	}
	if https {
		files = append(files, goldenFile{path: "/etc/nginx/zimam/tls.conf", tmpl: "nginx/tls.conf"})
	}
	return files
}

func units() []goldenFile {
	var p paths.Paths
	var files []goldenFile
	for _, name := range []string{"zimam-panel-queue.service", "zimam-panel-scheduler.service", "zimam-panel-scheduler.timer", "zimam-maintenance.service", "zimam-maintenance.timer"} {
		files = append(files, goldenFile{path: p.SystemdUnit(name), tmpl: "systemd/" + name, data: func() any { return PanelUnits{PanelPHP: "8.4"} }})
	}
	return files
}

func logPolicy() []goldenFile {
	var p paths.Paths
	policy := func() any { return LogPolicy{RetentionDays: 14, AuditRetentionDays: 365} }
	return []goldenFile{
		{path: p.LogrotateConf(), tmpl: "logrotate/zimam.conf"},
		{path: p.OwnLogrotateConf(), tmpl: "logrotate/zimam-own.conf", data: policy},
		{path: p.SiteLogrotateConf("shop"), tmpl: "logrotate/site.conf", data: func() any { return SiteLogs{Name: "shop", RetentionDays: 14} }},
		{path: p.JournaldDropIn(), tmpl: "journald/50-zimam.conf", data: policy},
	}
}

func suryRepo(url, host string) []goldenFile {
	var p paths.Paths
	data := func() any { return SuryRepo{URL: url, Host: host} }
	return []goldenFile{
		{path: p.SurySources(), tmpl: "apt/zimam-sury.sources", data: data},
		{path: p.SuryPin(), tmpl: "apt/zimam-sury.pref", data: data},
	}
}

func phpBase() []goldenFile {
	var p paths.Paths
	var files []goldenFile
	for _, r := range contract.PHPAllowlist {
		files = append(files,
			goldenFile{path: p.PHPIni(r.Version), tmpl: "php/99-zimam.ini"},
			goldenFile{path: p.PHPGlobalConf(r.Version), tmpl: "php/00-zimam-global.conf"},
		)
	}
	return files
}

func TestGolden(t *testing.T) {
	if *update {
		if err := os.RemoveAll(goldenDir); err != nil {
			t.Fatal(err)
		}
	}
	for name, files := range goldenCases {
		for _, f := range files {
			if !path.IsAbs(f.path) || path.Clean(f.path) != f.path {
				t.Errorf("%s: %q isn't a clean absolute path", name, f.path)
				continue
			}
			out, err := f.render()
			if err != nil {
				t.Errorf("%s %s: %v", name, f.path, err)
				continue
			}
			golden := filepath.Join(goldenDir, name, filepath.FromSlash(f.path))
			if *update {
				if err := os.MkdirAll(filepath.Dir(golden), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(golden, out, 0o644); err != nil {
					t.Fatal(err)
				}
				continue
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Errorf("%s %s: %v (go test ./internal/templates -update writes it)", name, f.path, err)
				continue
			}
			if !bytes.Equal(out, want) {
				t.Errorf("%s %s differs from %s:\n--- rendered\n%s--- golden\n%s", name, f.path, golden, out, want)
			}
		}
	}
	// configcheck tests every file under testdata/golden, so none may be left
	// over from a case or file that is gone.
	err := filepath.WalkDir(goldenDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(goldenDir, p)
		if err != nil {
			return err
		}
		name, file, _ := strings.Cut(filepath.ToSlash(rel), "/")
		if !slices.ContainsFunc(goldenCases[name], func(f goldenFile) bool { return f.path == "/"+file }) {
			t.Errorf("%s belongs to no golden case (go test ./internal/templates -update removes it)", p)
		}
		return nil
	})
	if err != nil {
		t.Error(err)
	}
}

// stringsIn returns an addressable copy of data and every settable string in
// it: struct fields, slice and array elements, and what pointers point at.
func stringsIn(data any) (reflect.Value, []reflect.Value) {
	root := reflect.New(reflect.TypeOf(data)).Elem()
	root.Set(reflect.ValueOf(data))
	var found []reflect.Value
	var walk func(v reflect.Value)
	walk = func(v reflect.Value) {
		switch v.Kind() {
		case reflect.String:
			if v.CanSet() {
				found = append(found, v)
			}
		case reflect.Struct:
			for i := range v.NumField() {
				walk(v.Field(i))
			}
		case reflect.Slice, reflect.Array:
			for i := range v.Len() {
				walk(v.Index(i))
			}
		case reflect.Pointer:
			if !v.IsNil() {
				walk(v.Elem())
			}
		}
	}
	walk(root)
	return root, found
}

func TestStringsInFindsNestedStrings(t *testing.T) {
	type inner struct {
		Name contract.SiteName
		Tags []string
	}
	data := struct {
		Domain contract.Domain
		Port   int
		Inner  *inner
		Users  []contract.BasicAuthUser
		hidden string
	}{
		Inner: &inner{Tags: []string{"a", "b"}},
		Users: []contract.BasicAuthUser{{}},
	}
	// Domain, Inner.Name, the two tags, Username and PasswordHash.
	if _, found := stringsIn(data); len(found) != 6 {
		t.Errorf("found %d strings, want 6", len(found))
	}
}

// renderedString is one string in a golden file's data that its template
// writes into the output.
type renderedString struct {
	what  string
	file  goldenFile
	index int // into stringsIn(file.data())
}

// renderedStrings finds every string a golden case's template renders: a
// sentinel put there shows up in the output. A field the template doesn't
// use, such as SuryRepo.Host in the source file, is left out.
func renderedStrings(t testing.TB) []renderedString {
	// A string a template checks again before writing it, such as a
	// bcrypt hash in htpasswd, takes the sentinel in its own shape.
	sentinels := []string{"zimamsentinel", "$2y$12$zimamsentinel" + strings.Repeat("a", 40)}
	var found []renderedString
	for name, files := range goldenCases {
		for _, gf := range files {
			if gf.data == nil {
				continue
			}
			_, fields := stringsIn(gf.data())
			for i := range fields {
				var errs []string
				for _, sentinel := range sentinels {
					root, fields := stringsIn(gf.data())
					fields[i].SetString(sentinel)
					out, err := Render(gf.tmpl, root.Interface())
					if err != nil {
						errs = append(errs, err.Error())
						continue
					}
					if bytes.Contains(out, []byte(sentinel)) {
						found = append(found, renderedString{fmt.Sprintf("%s %s (string %d)", name, gf.tmpl, i), gf, i})
					}
					break
				}
				if len(errs) == len(sentinels) {
					t.Fatalf("%s %s refuses every sentinel in string %d: %s", name, gf.tmpl, i, strings.Join(errs, "; "))
				}
			}
		}
	}
	return found
}

// FuzzRenderSafe puts random strings through safe, and into every string a
// golden case's template renders. Rendering either fails, or the value
// appears exactly as given and adds no line break.
func FuzzRenderSafe(f *testing.F) {
	for _, s := range []string{"shop.example.com", "packages.sury.org", "a;b", "a\nb", "a{b", "$host", `a"b`, "a b", ""} {
		f.Add(s)
	}
	set := testSet(f, map[string]string{"test/safe.conf": "server_name {{safe .}};\n"})
	targets := renderedStrings(f)
	f.Fuzz(func(t *testing.T, s string) {
		check := func(what string, out []byte, err error) {
			if err != nil {
				return
			}
			if strings.ContainsAny(s, "\r\n") {
				t.Errorf("%s rendered a value with a line break: %q", what, s)
			}
			if !bytes.Contains(out, []byte(s)) {
				t.Errorf("%s didn't render %q as given:\n%s", what, s, out)
			}
		}
		out, err := render(set, "test/safe.conf", s)
		check("safe", out, err)
		for _, target := range targets {
			root, fields := stringsIn(target.file.data())
			fields[target.index].SetString(s)
			out, err := Render(target.file.tmpl, root.Interface())
			check(target.what, out, err)
		}
	})
}

func TestRenderedStringsFindsTemplateFields(t *testing.T) {
	found := map[string]bool{}
	for _, r := range renderedStrings(t) {
		found[r.what] = true
	}
	// SuryRepo's string 0 is URL, which only the source file renders; string
	// 1 is Host, which only the pin renders.
	for _, c := range []string{"apt-sury", "apt-sury-mirror"} {
		for what, want := range map[string]bool{
			c + " apt/zimam-sury.sources (string 0)": true,
			c + " apt/zimam-sury.sources (string 1)": false,
			c + " apt/zimam-sury.pref (string 0)":    false,
			c + " apt/zimam-sury.pref (string 1)":    true,
		} {
			if found[what] != want {
				t.Errorf("%s: found = %v, want %v", what, found[what], want)
			}
		}
	}
}
