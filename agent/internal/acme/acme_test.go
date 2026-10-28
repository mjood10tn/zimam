package acme

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"zimam/agent/internal/config"
	"zimam/agent/internal/paths"
	"zimam/agent/internal/sysexec"
)

type fakeResolver struct {
	addrs []net.IPAddr
	err   error
}

func (r fakeResolver) LookupIPAddr(context.Context, string) ([]net.IPAddr, error) {
	return r.addrs, r.err
}

var v4 = []net.IPAddr{{IP: net.ParseIP("192.0.2.10")}}

// testDeps is a server under a temporary root that resolves the domain to
// addrs, with a FakeRunner and a log the test can read.
func testDeps(t *testing.T, addrs []net.IPAddr) (sysexec.Deps, *sysexec.FakeRunner, *[]string) {
	t.Helper()
	cfg := config.New("panel.example.com", "8.4", "8.4")
	run := sysexec.NewFakeRunner()
	var logs []string
	d := sysexec.Deps{
		Paths:  paths.Paths{Root: t.TempDir()},
		Run:    run,
		Config: cfg,
		Logf:   func(format string, args ...any) { logs = append(logs, fmt.Sprintf(format, args...)) },
		Now:    time.Now,
		DNS:    fakeResolver{addrs: addrs},
	}
	return d, run, &logs
}

// certbotArgs is §10's command line for zimam-shop, with the email part and
// any extra arguments given.
func certbotArgs(d sysexec.Deps, email []string, extra ...string) []string {
	args := []string{"certonly", "--webroot", "-w", d.Paths.ACMEWebroot(), "--cert-name", "zimam-shop", "-d", "shop.example.com",
		"--non-interactive", "--agree-tos"}
	args = append(args, email...)
	args = append(args, "--server", d.Config.ACMEDirectory, "--deploy-hook", d.Paths.AgentBinary()+" hook cert-deployed")
	return append(args, extra...)
}

// writeCert writes a self-signed certificate valid until notAfter, as
// certbot leaves cert.pem in the lineage.
func writeCert(t *testing.T, path string, notAfter time.Time) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "shop.example.com"},
		NotBefore:    notAfter.Add(-90 * 24 * time.Hour),
		NotAfter:     notAfter,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil {
		t.Fatal(err)
	}
}

var shop = Request{CertName: "zimam-shop", Domain: "shop.example.com"}

func TestIssueNoRecords(t *testing.T) {
	for name, r := range map[string]fakeResolver{
		"no addresses": {},
		"no such host": {err: &net.DNSError{Err: "no such host", Name: "shop.example.com", IsNotFound: true}},
	} {
		d, run, _ := testDeps(t, nil)
		d.DNS = r
		if _, err := Issue(context.Background(), d, shop); err == nil {
			t.Errorf("%s: Issue succeeded", name)
		}
		if calls := run.Calls(); len(calls) != 0 {
			t.Errorf("%s: ran certbot: %q", name, calls[0].Args)
		}
	}
}

func TestIssueIPv6Notice(t *testing.T) {
	for _, c := range []struct {
		addrs  []net.IPAddr
		notice bool
	}{
		{v4, false},
		{append(slices.Clone(v4), net.IPAddr{IP: net.ParseIP("2001:db8::10")}), true},
	} {
		d, run, logs := testDeps(t, c.addrs)
		run.Add(sysexec.Script{Path: "/usr/bin/certbot", Args: certbotArgs(d, []string{"--register-unsafely-without-email"}, "--dry-run")})
		dry := shop
		dry.DryRun = true
		if _, err := Issue(context.Background(), d, dry); err != nil {
			t.Fatalf("Issue: %v", err)
		}
		said := slices.ContainsFunc(*logs, func(l string) bool { return strings.Contains(l, "IPv6") })
		if said != c.notice {
			t.Errorf("with %v, the IPv6 notice was given: %v; the log: %q", c.addrs, said, *logs)
		}
	}
}

func TestCertbotArgsWithEmail(t *testing.T) {
	d, run, _ := testDeps(t, v4)
	d.Config.ACMEEmail = "admin@example.com"
	writeCert(t, d.Paths.LetsEncryptCert("zimam-shop"), time.Now().Add(90*24*time.Hour))
	run.Add(sysexec.Script{Path: "/usr/bin/certbot", Args: certbotArgs(d, []string{"--email", "admin@example.com"})})
	if _, err := Issue(context.Background(), d, shop); err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if calls := run.Calls(); len(calls) != 1 || calls[0].Path != "/usr/bin/certbot" {
		t.Errorf("ran %+v, want certbot once", calls)
	}
}

func TestCertbotArgsWithoutEmail(t *testing.T) {
	d, run, _ := testDeps(t, v4)
	writeCert(t, d.Paths.LetsEncryptCert("zimam-shop"), time.Now().Add(90*24*time.Hour))
	run.Add(sysexec.Script{Path: "/usr/bin/certbot", Args: certbotArgs(d, []string{"--register-unsafely-without-email"})})
	if _, err := Issue(context.Background(), d, shop); err != nil {
		t.Fatalf("Issue: %v", err)
	}
}

func TestCABundleEnv(t *testing.T) {
	for bundle, want := range map[string][]string{
		"":                      nil,
		"/etc/zimam/pebble.pem": {"REQUESTS_CA_BUNDLE=/etc/zimam/pebble.pem"},
	} {
		d, run, _ := testDeps(t, v4)
		d.Config.ACMECABundle = bundle
		run.Add(sysexec.Script{Path: "/usr/bin/certbot", Args: certbotArgs(d, []string{"--register-unsafely-without-email"}, "--dry-run")})
		dry := shop
		dry.DryRun = true
		if _, err := Issue(context.Background(), d, dry); err != nil {
			t.Fatalf("Issue: %v", err)
		}
		if calls := run.Calls(); len(calls) != 1 {
			t.Errorf("bundle %q: %d calls, want certbot once", bundle, len(calls))
		} else if !slices.Equal(calls[0].Env, want) {
			t.Errorf("bundle %q: certbot ran with env %q, want %q", bundle, calls[0].Env, want)
		}
	}
}

func TestDryRunArg(t *testing.T) {
	// A dry run saves no certificate, so there is no expiry to read: none of
	// cert.pem exists here.
	d, run, _ := testDeps(t, v4)
	run.Add(sysexec.Script{Path: "/usr/bin/certbot", Args: certbotArgs(d, []string{"--register-unsafely-without-email"}, "--dry-run")})
	dry := shop
	dry.DryRun = true
	expiry, err := Issue(context.Background(), d, dry)
	if err != nil || !expiry.IsZero() {
		t.Errorf("Issue = %v, %v; want the zero time and no error", expiry, err)
	}
}

func TestIssueReturnsExpiry(t *testing.T) {
	d, run, _ := testDeps(t, v4)
	notAfter := time.Date(2027, 1, 26, 12, 0, 0, 0, time.UTC)
	writeCert(t, d.Paths.LetsEncryptCert("zimam-shop"), notAfter)
	run.Add(sysexec.Script{Path: "/usr/bin/certbot", Args: certbotArgs(d, []string{"--register-unsafely-without-email"})})
	expiry, err := Issue(context.Background(), d, shop)
	if err != nil || !expiry.Equal(notAfter) {
		t.Errorf("Issue = %v, %v; want %v", expiry, err, notAfter)
	}
}

func TestFailureReturnsLastLines(t *testing.T) {
	d, run, _ := testDeps(t, v4)
	var out strings.Builder
	for i := 1; i <= 30; i++ {
		fmt.Fprintf(&out, "certbot line %d\n", i)
	}
	run.Add(sysexec.Script{Path: "/usr/bin/certbot", Args: certbotArgs(d, []string{"--register-unsafely-without-email"}),
		Result: sysexec.Result{ExitCode: 1, Stderr: []byte(out.String())}})
	_, err := Issue(context.Background(), d, shop)
	if err == nil {
		t.Fatal("Issue succeeded although certbot failed")
	}
	for i := 11; i <= 30; i++ {
		if !strings.Contains(err.Error(), fmt.Sprintf("certbot line %d\n", i)) && !strings.HasSuffix(err.Error(), fmt.Sprintf("certbot line %d", i)) {
			t.Errorf("the error lacks line %d: %v", i, err)
		}
	}
	if strings.Contains(err.Error(), "certbot line 10\n") {
		t.Errorf("the error has more than the last 20 lines: %v", err)
	}
}

func TestIssueRefusesBadRequests(t *testing.T) {
	for _, r := range []Request{
		{CertName: "zimam-../x", Domain: "shop.example.com"},
		{CertName: "shop", Domain: "shop.example.com"},
		{CertName: "zimam-panel/", Domain: "shop.example.com"},
		{CertName: "zimam-shop", Domain: "shop.example.com --server https://evil.example"},
	} {
		d, run, _ := testDeps(t, v4)
		if _, err := Issue(context.Background(), d, r); err == nil {
			t.Errorf("Issue(%+v) succeeded", r)
		}
		if calls := run.Calls(); len(calls) != 0 {
			t.Errorf("%+v: ran certbot", r)
		}
	}
}

func TestReadExpiry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cert.pem")
	notAfter := time.Date(2027, 3, 1, 8, 30, 0, 0, time.UTC)
	writeCert(t, path, notAfter)
	if got, err := ReadExpiry(path); err != nil || !got.Equal(notAfter) {
		t.Errorf("ReadExpiry = %v, %v; want %v", got, err, notAfter)
	}
	if err := os.WriteFile(path, []byte("not a certificate\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, err := ReadExpiry(path); err == nil {
		t.Errorf("ReadExpiry of garbage = %v", got)
	}
}

func TestRecordMode0600(t *testing.T) {
	d, _, _ := testDeps(t, v4)
	expiry := time.Date(2027, 1, 26, 12, 0, 0, 0, time.UTC)
	for _, name := range []string{"panel", "shop"} {
		if err := WriteRecord(d, name, expiry); err != nil {
			t.Fatalf("WriteRecord(%s): %v", name, err)
		}
		path := d.Paths.CertRecord(name)
		fi, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != 0o600 {
			t.Errorf("%s: mode %v, want 0600", path, fi.Mode().Perm())
		}
		b, _ := os.ReadFile(path)
		var rec Record
		if err := json.Unmarshal(b, &rec); err != nil || rec.Name != name || !rec.Expiry.Equal(expiry) {
			t.Errorf("%s = %s (%v)", path, b, err)
		}
	}
	if fi, err := os.Stat(d.Paths.CertsDir()); err != nil || fi.Mode().Perm() != 0o700 {
		t.Errorf("the certs directory: %v, %v; want mode 0700", fi, err)
	}
	for _, bad := range []string{"../shop", "shop.json", "", "Panel"} {
		if err := WriteRecord(d, bad, expiry); err == nil {
			t.Errorf("WriteRecord(%q) succeeded", bad)
		}
	}
}
