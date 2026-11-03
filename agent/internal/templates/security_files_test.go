package templates

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The ACME location comes before the redirect, the IP rules and password
// protection (§9.3), so renewals work whatever the settings: in every
// server of the golden vhosts it is the first location and carries "allow
// all" and "auth_basic off", and security.conf, which holds the rules at
// server level, has no location that could come first.
func TestAcmeLocationBeforeAuthAndIpRules(t *testing.T) {
	for _, c := range []string{"security-ip-both", "security-password", "security-ip-allow"} {
		dir := filepath.Join("testdata", "golden", c)
		vhost, err := os.ReadFile(filepath.Join(dir, "etc/nginx/sites-available/zimam-shop.conf"))
		if err != nil {
			t.Fatal(err)
		}
		sec, err := os.ReadFile(filepath.Join(dir, "etc/nginx/zimam/shop/security.conf"))
		if err != nil {
			t.Fatal(err)
		}
		if regexp.MustCompile(`(?m)^\s*location\s`).Match(sec) {
			t.Errorf("%s: security.conf has a location:\n%s", c, sec)
		}
		if !strings.Contains(string(sec), "deny ") && !strings.Contains(string(sec), "auth_basic ") {
			t.Errorf("%s: security.conf holds no rule to check against:\n%s", c, sec)
		}
		servers := strings.Split(string(vhost), "server {")[1:]
		if len(servers) != 2 {
			t.Fatalf("%s: %d servers, want the redirect and the HTTPS one", c, len(servers))
		}
		for i, s := range servers {
			first := strings.Index(s, "    location ")
			if first < 0 || !strings.HasPrefix(s[first:], "    location ^~ /.well-known/acme-challenge/ {") {
				t.Errorf("%s server %d: the first location isn't the ACME one:\n%s", c, i+1, s)
				continue
			}
			body := s[first : first+strings.Index(s[first:], "}")]
			for _, want := range []string{"root /var/www/zimam-acme;", "allow all;", "auth_basic off;"} {
				if !strings.Contains(body, want) {
					t.Errorf("%s server %d: the ACME location lacks %q:\n%s", c, i+1, want, body)
				}
			}
		}
	}
}
