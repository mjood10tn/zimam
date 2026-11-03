package templates

import (
	"strings"
	"testing"
)

const securityInclude = "    include /etc/nginx/zimam/shop/security.conf;\n"

// Before its certificate exists, one HTTP server serves the site, with the
// site's security settings (§9.3, §9.6).
func TestSiteVhostHTTPOnly(t *testing.T) {
	for _, v := range []SiteVhost{{Name: "shop", Domain: "shop.example.com"}, {Name: "shop", Domain: "shop.example.com", PHPVersion: "8.4"}} {
		out := mustRender(t, "nginx/site.conf", v)
		if n := strings.Count(out, "server {"); n != 1 {
			t.Errorf("%d servers, want 1:\n%s", n, out)
		}
		contains(t, "the HTTP-only vhost", out, "    listen 80;\n", "    listen [::]:80;\n", "    server_name shop.example.com;\n",
			"    access_log /var/log/zimam/sites/shop/access.log;\n", "    error_log  /var/log/zimam/sites/shop/error.log;\n",
			securityInclude, "    root /home/s_shop/site/current/public;\n", "    autoindex off;\n", acmeLocation,
			"    location ~ /\\.(?!well-known) {\n        deny all;\n    }\n")
		lacks(t, "the HTTP-only vhost", out, "443", "ssl_certificate", "return 301")
	}
}

func TestSiteVhostStaticOrPHP(t *testing.T) {
	static := mustRender(t, "nginx/site.conf", SiteVhost{Name: "shop", Domain: "shop.example.com"})
	contains(t, "a static vhost", static, "    index index.html;\n", "        try_files $uri $uri/ =404;\n")
	lacks(t, "a static vhost", static, "fastcgi", "index.php")

	php := mustRender(t, "nginx/site.conf", SiteVhost{Name: "shop", Domain: "shop.example.com", PHPVersion: "8.4"})
	contains(t, "a PHP vhost", php, "    index index.php index.html;\n", "        try_files $uri $uri/ /index.php?$query_string;\n",
		"    location ~ \\.php$ {\n        fastcgi_pass unix:/run/php/zimam-shop-8.4.sock;\n        include fastcgi_params;\n"+
			"        fastcgi_param SCRIPT_FILENAME $realpath_root$fastcgi_script_name;\n        fastcgi_param DOCUMENT_ROOT $realpath_root;\n    }\n")
}

// With its certificate, the site is served over HTTPS too; the HTTP server
// redirects only when force_https is on, and keeps the ACME location first.
func TestSiteVhostHTTPS(t *testing.T) {
	out := mustRender(t, "nginx/site.conf", SiteVhost{Name: "shop", Domain: "shop.example.com", PHPVersion: "8.4", HTTPS: true})
	servers := strings.Split(out, "server {")
	if len(servers) != 3 {
		t.Fatalf("%d servers, want 2:\n%s", len(servers)-1, out)
	}
	contains(t, "the HTTPS server", servers[2], "    listen 443 ssl;\n", "    listen [::]:443 ssl;\n", "    http2 on;\n",
		"    ssl_certificate     /etc/letsencrypt/live/zimam-shop/fullchain.pem;\n",
		"    ssl_certificate_key /etc/letsencrypt/live/zimam-shop/privkey.pem;\n",
		"    include /etc/nginx/zimam/tls.conf;\n", securityInclude, "    root /home/s_shop/site/current/public;\n", acmeLocation)
	contains(t, "the HTTP server without force_https", servers[1], securityInclude, "    root /home/s_shop/site/current/public;\n")
	lacks(t, "the HTTP server without force_https", servers[1], "return 301")

	out = mustRender(t, "nginx/site.conf", SiteVhost{Name: "shop", Domain: "shop.example.com", HTTPS: true, ForceHTTPS: true})
	http := strings.Split(out, "server {")[1]
	if a, r := strings.Index(http, acmeLocation), strings.Index(http, "return 301 https://$host$request_uri;"); a < 0 || r < a {
		t.Errorf("the HTTP server with force_https wants the ACME location, then the redirect:\n%s", http)
	}
	lacks(t, "the HTTP server with force_https", http, "root /home/")
}

// security.conf holds §9.6's defaults until Task 33: the header preset on.
func TestSecurityConfDefaults(t *testing.T) {
	out := mustRender(t, "nginx/security.conf", SiteSecurity{HeadersPreset: true})
	contains(t, "the default security.conf", out,
		"add_header X-Content-Type-Options \"nosniff\" always;\n",
		"add_header X-Frame-Options \"SAMEORIGIN\" always;\n",
		"add_header Referrer-Policy \"strict-origin-when-cross-origin\" always;\n",
		"add_header Permissions-Policy \"camera=(), microphone=(), geolocation=()\" always;\n")
	lacks(t, "the default security.conf", out, "Strict-Transport-Security", "auth_basic", "deny", "allow")
	lacks(t, "security.conf without the preset", mustRender(t, "nginx/security.conf", SiteSecurity{}), "add_header")
}

func TestSitePool(t *testing.T) {
	out := mustRender(t, "php/site-pool.conf", SitePool{Name: "shop", PHPVersion: "8.4"})
	contains(t, "the pool", out, "[zimam-shop]\n", "user = s_shop\n", "group = s_shop\n", "listen = /run/php/zimam-shop-8.4.sock\n",
		"listen.owner = s_shop\n", "listen.group = www-data\n", "listen.mode = 0660\n", "pm = ondemand\n",
		"php_admin_value[error_log] = /home/s_shop/logs/php-error.log\n", "php_admin_value[session.save_path] = /home/s_shop/tmp\n")
	lacks(t, "the default pool", out, "open_basedir", "disable_functions")
	out = mustRender(t, "php/site-pool.conf", SitePool{Name: "shop", PHPVersion: "8.4", BlockRiskyFunctions: true, RestrictToSiteFolder: true})
	contains(t, "the strict pool", out,
		"php_admin_value[open_basedir] = /home/s_shop/site/:/home/s_shop/tmp/:/home/s_shop/logs/\n",
		"php_admin_value[disable_functions] = exec,passthru,shell_exec,system,proc_open,popen,pcntl_exec\n")
}

func TestPlaceholderPrintsPHPVersion(t *testing.T) {
	php := mustRender(t, "site/index.php", SitePlaceholder{Name: "shop"})
	contains(t, "index.php", php, "<?php", "PHP_VERSION", "Zimam site shop is ready")
	html := mustRender(t, "site/index.html", SitePlaceholder{Name: "shop"})
	contains(t, "index.html", html, "Zimam site shop is ready")
	lacks(t, "index.html", html, "<?php")
}
