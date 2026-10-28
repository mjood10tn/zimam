package paths

import (
	"reflect"
	"strings"
	"testing"

	"zimam/agent/contract"
)

// production is every method's path with Root "", for site "shop", PHP 8.4,
// job testJob and subject "site:shop" (design §5.5, §5.7, §5.9, §7.1, §8.2,
// §8.3, §9.1, §9.3, §10, §11.3, D15, D17).
var production = map[string]string{
	"AgentBinary":         "/usr/local/sbin/zimam-agent",
	"ConfigDir":           "/etc/zimam",
	"AgentConfig":         "/etc/zimam/agent.json",
	"InstallerState":      "/etc/zimam/installer.state",
	"LogrotateConf":       "/etc/zimam/logrotate.conf",
	"LogrotateDir":        "/etc/zimam/logrotate.d",
	"SiteLogrotateConf":   "/etc/zimam/logrotate.d/site-shop.conf",
	"RunDir":              "/run/zimam",
	"Socket":              "/run/zimam/agent.sock",
	"LocksDir":            "/run/zimam/locks",
	"SubmitLock":          "/run/zimam/locks/submit.lock",
	"AuditLock":           "/run/zimam/locks/audit.lock",
	"ReloadLock":          "/run/zimam/locks/reload.lock",
	"SubjectLock":         "/run/zimam/locks/site:shop.lock",
	"StateDir":            "/var/lib/zimam",
	"JobsDir":             "/var/lib/zimam/jobs",
	"JobDir":              "/var/lib/zimam/jobs/" + testJob,
	"JobSpec":             "/var/lib/zimam/jobs/" + testJob + "/spec.json",
	"JobState":            "/var/lib/zimam/jobs/" + testJob + "/state.json",
	"SitesStateDir":       "/var/lib/zimam/sites",
	"SiteState":           "/var/lib/zimam/sites/shop.json",
	"DownloadsDir":        "/var/lib/zimam/downloads",
	"CertsDir":            "/var/lib/zimam/certs",
	"UpdatesCache":        "/var/lib/zimam/updates.json",
	"LogrotateState":      "/var/lib/zimam/logrotate.state",
	"LogDir":              "/var/log/zimam",
	"AuditLog":            "/var/log/zimam/audit.log",
	"JobLogsDir":          "/var/log/zimam/jobs",
	"JobLog":              "/var/log/zimam/jobs/" + testJob + ".log",
	"SitesLogDir":         "/var/log/zimam/sites",
	"SiteLogDir":          "/var/log/zimam/sites/shop",
	"SiteAccessLog":       "/var/log/zimam/sites/shop/access.log",
	"SiteErrorLog":        "/var/log/zimam/sites/shop/error.log",
	"ACMEWebroot":         "/var/www/zimam-acme",
	"NginxZimamDir":       "/etc/nginx/zimam",
	"SiteNginxDir":        "/etc/nginx/zimam/shop",
	"SiteSecurityConf":    "/etc/nginx/zimam/shop/security.conf",
	"SiteHtpasswd":        "/etc/nginx/zimam/shop/htpasswd",
	"NginxSiteAvailable":  "/etc/nginx/sites-available/zimam-shop.conf",
	"NginxSiteEnabled":    "/etc/nginx/sites-enabled/zimam-shop.conf",
	"PHPPool":             "/etc/php/8.4/fpm/pool.d/zimam-shop.conf",
	"PHPSocket":           "/run/php/zimam-shop-8.4.sock",
	"PHPPoolDir":          "/etc/php/8.4/fpm/pool.d",
	"PHPIni":              "/etc/php/8.4/fpm/conf.d/99-zimam.ini",
	"PHPGlobalConf":       "/etc/php/8.4/fpm/pool.d/00-zimam-global.conf",
	"PHPWwwPool":          "/etc/php/8.4/fpm/pool.d/www.conf",
	"SuryKeyring":         "/etc/apt/keyrings/zimam-sury.gpg",
	"SurySources":         "/etc/apt/sources.list.d/zimam-sury.sources",
	"SuryPin":             "/etc/apt/preferences.d/zimam-sury.pref",
	"NginxPanelAvailable": "/etc/nginx/sites-available/zimam-panel.conf",
	"NginxPanelEnabled":   "/etc/nginx/sites-enabled/zimam-panel.conf",
	"PanelPool":           "/etc/php/8.4/fpm/pool.d/zimam-panel.conf",
	"SystemdUnit":         "/etc/systemd/system/site:shop", // the test passes every string argument "site:shop"
	"JournaldDropIn":      "/etc/systemd/journald.conf.d/50-zimam.conf",
	"OwnLogrotateConf":    "/etc/zimam/logrotate.d/zimam-own.conf",
	"LetsEncryptCert":     "/etc/letsencrypt/live/site:shop/cert.pem",
	"CertRecord":          "/var/lib/zimam/certs/site:shop.json",
	"PanelHome":           "/home/zimam",
	"PanelReleasesDir":    "/home/zimam/panel/releases",
	"PanelCurrent":        "/home/zimam/panel/current",
	"PanelSharedDir":      "/home/zimam/panel/shared",
	"SiteHome":            "/home/s_shop",
	"SiteDir":             "/home/s_shop/site",
	"SiteReleasesDir":     "/home/s_shop/site/releases",
	"SiteCurrent":         "/home/s_shop/site/current",
	"SiteTmpDir":          "/home/s_shop/tmp",
	"SiteHomeLogsDir":     "/home/s_shop/logs",
	"SitePHPErrorLog":     "/home/s_shop/logs/php-error.log",
}

const testJob = "01JBX7Q2M5YQ8T3N6V0R4K9W1E"

// callAll calls every method of p with the test arguments and returns the
// results by method name.
func callAll(t *testing.T, p Paths) map[string]string {
	t.Helper()
	args := map[reflect.Type]reflect.Value{
		reflect.TypeFor[contract.SiteName]():   reflect.ValueOf(contract.SiteName("shop")),
		reflect.TypeFor[contract.PHPVersion](): reflect.ValueOf(contract.PHPVersion("8.4")),
		reflect.TypeFor[contract.JobID]():      reflect.ValueOf(contract.JobID(testJob)),
		reflect.TypeFor[string]():              reflect.ValueOf("site:shop"),
	}
	v := reflect.ValueOf(p)
	got := map[string]string{}
	for i := range v.NumMethod() {
		m := v.Type().Method(i)
		in := make([]reflect.Value, m.Type.NumIn()-1)
		for j := range in {
			a, ok := args[m.Type.In(j+1)]
			if !ok {
				t.Fatalf("%s: no test argument for %v", m.Name, m.Type.In(j+1))
			}
			in[j] = a
		}
		out := v.Method(i).Call(in)
		if len(out) != 1 || out[0].Kind() != reflect.String {
			t.Fatalf("%s does not return one string", m.Name)
		}
		got[m.Name] = out[0].String()
	}
	return got
}

func TestPathsProduction(t *testing.T) {
	got := callAll(t, Paths{})
	for name, want := range production {
		if got[name] != want {
			t.Errorf("%s() = %q, want %q", name, got[name], want)
		}
	}
	for name := range got {
		if _, ok := production[name]; !ok {
			t.Errorf("%s has no expected path in the test table", name)
		}
	}
}

func TestPathsUnderRoot(t *testing.T) {
	root := t.TempDir()
	for name, got := range callAll(t, Paths{Root: root}) {
		if !strings.HasPrefix(got, root+"/") {
			t.Errorf("%s() = %q, not under %q", name, got, root)
		}
		if want := root + production[name]; got != want {
			t.Errorf("%s() = %q, want %q", name, got, want)
		}
	}
}
