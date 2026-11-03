package sites

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"zimam/agent/contract"
)

var certNotAfter = time.Date(2027, 2, 1, 12, 0, 0, 0, time.UTC)

type oneAddress struct{}

func (oneAddress) LookupIPAddr(context.Context, string) ([]net.IPAddr, error) {
	return []net.IPAddr{{IP: net.ParseIP("203.0.113.10")}}, nil
}

// certEnv is a server with the static site shop provisioned, its domain
// resolving, and certbot answering as env.certbot says.
func certEnv(t *testing.T) *env {
	t.Helper()
	e := newEnv(t)
	if _, err := e.provision(static); err != nil {
		t.Fatalf("provisioning shop: %v", err)
	}
	e.d.DNS = oneAddress{}
	return e
}

func (e *env) issue() (contract.CertIssueResult, error) {
	return IssueCertificate(context.Background(), e.d, "shop")
}

// writeCert writes cert.pem for certName, valid until certNotAfter, as
// certbot leaves it in the lineage.
func writeCert(t *testing.T, path string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "shop.example.com"}, NotBefore: certNotAfter.AddDate(0, -3, 0), NotAfter: certNotAfter}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	mkdir(t, filepath.Dir(path))
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil {
		t.Fatal(err)
	}
}

func certbotRan(calls []string) bool {
	return slices.ContainsFunc(calls, func(c string) bool { return strings.HasPrefix(c, "/usr/bin/certbot certonly ") })
}

// A certificate turns the site to HTTPS: the HTTP server redirects, the
// HTTPS server serves the site, and the expiry is recorded (§10 step 3).
func TestIssueSuccessSwitchesToHttps(t *testing.T) {
	e := certEnv(t)
	n := len(e.run.Calls())
	res, err := e.issue()
	if err != nil {
		t.Fatalf("IssueCertificate: %v", err)
	}
	if !res.ExpiresAt.Equal(certNotAfter) {
		t.Errorf("expires_at = %v, want %v", res.ExpiresAt, certNotAfter)
	}
	got := e.callsSince(n)
	if len(got) != 3 || !strings.HasPrefix(got[0], "/usr/bin/certbot certonly --webroot -w "+e.p.ACMEWebroot()+" --cert-name zimam-shop -d shop.example.com ") || got[1] != nginxTest || got[2] != nginxReload {
		t.Errorf("ran\n%s\nwant certbot, then nginx -t and a reload", strings.Join(got, "\n"))
	}
	vhost := string(e.read(e.p.NginxSiteAvailable("shop")))
	for _, want := range []string{"listen 443 ssl;", "ssl_certificate     /etc/letsencrypt/live/zimam-shop/fullchain.pem;", "return 301 https://$host$request_uri;"} {
		if !strings.Contains(vhost, want) {
			t.Errorf("the vhost lacks %q:\n%s", want, vhost)
		}
	}
	b, err := os.ReadFile(e.p.CertRecord("shop"))
	if err != nil || !strings.Contains(string(b), certNotAfter.Format(time.RFC3339)) {
		t.Errorf("expiry record = %s, %v", b, err)
	}
}

func TestForceHttpsStored(t *testing.T) {
	e := certEnv(t)
	if _, err := e.issue(); err != nil {
		t.Fatal(err)
	}
	st, err := Load(e.p, "shop")
	if err != nil || !st.Security.ForceHTTPS {
		t.Errorf("state = %+v, %v; want force_https on", st.Security, err)
	}
}

// When certbot fails, the site keeps its HTTP vhost and the job shows
// certbot's last lines (§10 step 4).
func TestIssueFailureKeepsHttp(t *testing.T) {
	e := certEnv(t)
	e.certbotFails = "Certbot failed to authenticate some domains (authenticator: webroot).\nDetail: 203.0.113.10: Fetching http://shop.example.com/.well-known/acme-challenge/x: Timeout"
	vhost, state := e.read(e.p.NginxSiteAvailable("shop")), e.read(e.p.SiteState("shop"))
	n := len(e.run.Calls())
	_, err := e.issue()
	if err == nil || !strings.Contains(err.Error(), "Fetching http://shop.example.com/.well-known/acme-challenge/x: Timeout") {
		t.Fatalf("IssueCertificate = %v, want certbot's last lines", err)
	}
	if got := e.callsSince(n); slices.Contains(got, nginxReload) {
		t.Errorf("Nginx was reloaded after certbot failed: %q", got)
	}
	if !bytes.Equal(e.read(e.p.NginxSiteAvailable("shop")), vhost) || !bytes.Equal(e.read(e.p.SiteState("shop")), state) {
		t.Error("the vhost or state file changed")
	}
	if exists(e.p.CertRecord("shop")) {
		t.Error("an expiry record was written")
	}
}

// When Nginx refuses the HTTPS vhost, the HTTP one is back and force_https
// stays off, so a retry tries again.
func TestHttpsVhostRollbackOnNginxFailure(t *testing.T) {
	e := certEnv(t)
	vhost := e.read(e.p.NginxSiteAvailable("shop"))
	failNginx(e)
	_, err := e.issue()
	if err == nil || !strings.Contains(err.Error(), "unknown directive") {
		t.Fatalf("IssueCertificate = %v, want nginx -t's output", err)
	}
	if !bytes.Equal(e.read(e.p.NginxSiteAvailable("shop")), vhost) {
		t.Error("the HTTP vhost wasn't restored")
	}
	if st, _ := Load(e.p, "shop"); st.Security.ForceHTTPS {
		t.Error("force_https was stored")
	}

	// The retry: certbot keeps the certificate it has (no new order), and
	// the HTTPS vhost goes in.
	if _, err := e.issue(); err != nil {
		t.Fatalf("the retry: %v", err)
	}
	if !strings.Contains(string(e.read(e.p.NginxSiteAvailable("shop"))), "listen 443 ssl;") {
		t.Error("the retry didn't switch to HTTPS")
	}
}

func TestIssueCheck(t *testing.T) {
	e := newEnv(t)
	wantCode(t, CheckCertificate(e.d, "shop"), contract.ErrNotFound)
	if _, err := e.provision(static); err != nil {
		t.Fatal(err)
	}
	if err := CheckCertificate(e.d, "shop"); err != nil {
		t.Errorf("a provisioned site: %v", err)
	}
}
