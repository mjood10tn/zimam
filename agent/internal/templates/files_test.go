package templates

import (
	"strings"
	"testing"
)

// mustRender renders an embedded template or fails the test.
func mustRender(t *testing.T, name string, data any) string {
	t.Helper()
	out, err := Render(name, data)
	if err != nil {
		t.Fatalf("Render(%s): %v", name, err)
	}
	return string(out)
}

func contains(t *testing.T, what, out string, want ...string) {
	t.Helper()
	for _, w := range want {
		if !strings.Contains(out, w) {
			t.Errorf("%s lacks %q:\n%s", what, w, out)
		}
	}
}

func lacks(t *testing.T, what, out string, unwanted ...string) {
	t.Helper()
	for _, u := range unwanted {
		if strings.Contains(out, u) {
			t.Errorf("%s has %q:\n%s", what, u, out)
		}
	}
}

const acmeLocation = "    location ^~ /.well-known/acme-challenge/ {\n        root /var/www/zimam-acme;\n        default_type text/plain;\n        allow all;\n        auth_basic off;\n    }\n"

func TestPanelVhostHTTPOnly(t *testing.T) {
	// Before the certificate exists, one HTTP server serves the panel (§9.3).
	out := mustRender(t, "nginx/panel.conf", PanelVhost{Domain: "panel.example.com"})
	if n := strings.Count(out, "server {"); n != 1 {
		t.Errorf("%d servers, want 1:\n%s", n, out)
	}
	contains(t, "the HTTP-only vhost", out, "    listen 80;\n", "    listen [::]:80;\n", "    server_name panel.example.com;\n",
		"    root /home/zimam/panel/current/public;\n", acmeLocation, "        fastcgi_pass unix:/run/php/zimam-panel.sock;\n")
	lacks(t, "the HTTP-only vhost", out, "443", "ssl_certificate", "return 301")
}

func TestPanelVhostHTTPSRedirects(t *testing.T) {
	out := mustRender(t, "nginx/panel.conf", PanelVhost{Domain: "panel.example.com", HTTPS: true})
	servers := strings.Split(out, "server {")[1:]
	if len(servers) != 2 {
		t.Fatalf("%d servers, want 2:\n%s", len(servers), out)
	}
	httpServer, httpsServer := servers[0], servers[1]
	contains(t, "the HTTP server", httpServer, "    listen 80;\n", "    listen [::]:80;\n", acmeLocation,
		"    location / {\n        return 301 https://$host$request_uri;\n    }\n")
	if strings.Index(httpServer, acmeLocation) > strings.Index(httpServer, "return 301") {
		t.Error("the ACME location must come before the redirect, so renewals keep working")
	}
	lacks(t, "the HTTP server", httpServer, "fastcgi_pass", "root /home/")
	contains(t, "the HTTPS server", httpsServer, "    listen 443 ssl;\n", "    listen [::]:443 ssl;\n", "    http2 on;\n",
		"    server_name panel.example.com;\n",
		"    ssl_certificate     /etc/letsencrypt/live/zimam-panel/fullchain.pem;\n",
		"    ssl_certificate_key /etc/letsencrypt/live/zimam-panel/privkey.pem;\n",
		"    include /etc/nginx/zimam/tls.conf;\n", "    root /home/zimam/panel/current/public;\n", acmeLocation,
		"    location ~ /\\.(?!well-known) {\n        deny all;\n    }\n",
		"        try_files $uri $uri/ /index.php?$query_string;\n",
		"        fastcgi_pass unix:/run/php/zimam-panel.sock;\n",
		"        fastcgi_param SCRIPT_FILENAME $realpath_root$fastcgi_script_name;\n")
}

func TestPanelPoolGolden(t *testing.T) {
	out := mustRender(t, "php/panel-pool.conf", nil)
	contains(t, "the panel pool", out, "[zimam-panel]\n", "user = zimam\n", "group = zimam\n",
		"listen = /run/php/zimam-panel.sock\n", "listen.owner = zimam\n", "listen.group = www-data\n", "listen.mode = 0660\n",
		"pm = ondemand\n", "php_admin_value[error_log] = /home/zimam/panel/shared/storage/logs/php-error.log\n")
	// Its logs and temporary files stay in the panel's storage folder.
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, "/home/") && !strings.Contains(l, "= /home/zimam/panel/shared/storage/") {
			t.Errorf("%q points outside /home/zimam/panel/shared/storage/", l)
		}
	}
}

func TestUnitsGolden(t *testing.T) {
	data := PanelUnits{PanelPHP: "8.4"}
	queue := mustRender(t, "systemd/zimam-panel-queue.service", data)
	contains(t, "the queue unit", queue, "User=zimam\n", "WorkingDirectory=/home/zimam/panel/current\n",
		"ExecStart=/usr/bin/php8.4 artisan queue:work --sleep=1 --max-time=3600\n", "Restart=always\n", "WantedBy=multi-user.target\n")
	scheduler := mustRender(t, "systemd/zimam-panel-scheduler.service", data)
	contains(t, "the scheduler service", scheduler, "User=zimam\n", "WorkingDirectory=/home/zimam/panel/current\n",
		"ExecStart=/usr/bin/php8.4 artisan schedule:run\n")
	contains(t, "the scheduler timer", mustRender(t, "systemd/zimam-panel-scheduler.timer", data), "OnCalendar=*-*-* *:*:00\n", "WantedBy=timers.target\n")
	contains(t, "the maintenance service", mustRender(t, "systemd/zimam-maintenance.service", data), "ExecStart=/usr/local/sbin/zimam-agent maintenance\n")
	contains(t, "the maintenance timer", mustRender(t, "systemd/zimam-maintenance.timer", data), "OnCalendar=*:0/15\n", "Persistent=true\n", "WantedBy=timers.target\n")
}

func TestSiteLogrotateGolden(t *testing.T) {
	// §11.3 exactly, with maxage from the retention setting.
	out := mustRender(t, "logrotate/site.conf", SiteLogs{Name: "shop", RetentionDays: 30})
	want := `/var/log/zimam/sites/shop/*.log {
    daily
    maxsize 200M
    rotate 1000
    maxage 30
    missingok
    notifempty
    compress
    dateext
    dateformat -%Y%m%d-%s
    sharedscripts
    postrotate
        /usr/sbin/nginx -s reopen >/dev/null 2>&1 || true
    endscript
}

/home/s_shop/logs/*.log {
    su s_shop s_shop
    daily
    maxsize 200M
    rotate 1000
    maxage 30
    missingok
    notifempty
    compress
    dateext
    dateformat -%Y%m%d-%s
}
`
	if _, rules, _ := strings.Cut(out, "\n"); rules != want {
		t.Errorf("the site's rules after the header line:\n%s\nwant:\n%s", rules, want)
	}
}

func TestOwnLogrotateUsesSuZimam(t *testing.T) {
	out := mustRender(t, "logrotate/zimam-own.conf", LogPolicy{RetentionDays: 7})
	audit, panelLogs, found := strings.Cut(out, "/home/zimam/panel/shared/storage/logs/*.log {")
	if !found {
		t.Fatalf("no rule for the panel's Laravel logs:\n%s", out)
	}
	contains(t, "the audit log's rule", audit, "/var/log/zimam/audit.log {\n", "    maxage 7\n", "    compress\n")
	lacks(t, "the audit log's rule", audit, "su ")
	contains(t, "the panel logs' rule", panelLogs, "\n    su zimam zimam\n", "    maxage 7\n", "    compress\n")
}

func TestJournaldDropIn(t *testing.T) {
	out := mustRender(t, "journald/50-zimam.conf", LogPolicy{RetentionDays: 21})
	contains(t, "the journald drop-in", out, "[Journal]\n", "SystemMaxFileSize=200M\n", "MaxRetentionSec=21day\n")
}

func TestLogrotateConfIgnoresStagedFiles(t *testing.T) {
	// logrotate reads every file in an included directory, and configtx
	// keeps <name>.zimam-prev beside the new file while logrotate -d tests
	// it: without the taboo, both rules count, and the duplicate fails.
	out := mustRender(t, "logrotate/zimam.conf", nil)
	taboo := strings.Index(out, "tabooext + .zimam-prev .zimam-tmp\n")
	include := strings.Index(out, "include /etc/zimam/logrotate.d\n")
	if taboo < 0 || include < 0 || taboo > include {
		t.Errorf("logrotate.conf must set the taboo before the include:\n%s", out)
	}
}
