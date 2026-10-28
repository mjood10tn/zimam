package main

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io/fs"
	"math/big"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"zimam/agent/internal/acme"
	"zimam/agent/internal/audit"
	"zimam/agent/internal/paths"
	"zimam/agent/internal/sysexec"
)

var (
	hookNginxTest = sysexec.Script{Path: "/usr/sbin/nginx", Args: []string{"-t"}}
	hookReload    = sysexec.Script{Path: "/usr/bin/systemctl", Args: []string{"reload-or-restart", "nginx"}}
	notAfter      = time.Date(2027, 1, 26, 12, 0, 0, 0, time.UTC)
)

// hookDeps is a server under a temporary root with what bootstrap creates.
// It has no /run/zimam/locks: /run is emptied at boot, and certbot's timer
// may run the hook before anything else has made it.
func hookDeps(t *testing.T, run sysexec.Runner) sysexec.Deps {
	t.Helper()
	p := paths.Paths{Root: t.TempDir()}
	for _, dir := range []string{p.LogDir(), p.SitesStateDir()} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	return sysexec.Deps{Paths: p, Run: run, Logf: t.Logf, Now: time.Now}
}

func lineage(value string) func(string) string {
	return func(key string) string {
		if key == "RENEWED_LINEAGE" {
			return value
		}
		return ""
	}
}

// writeHookCert writes cert.pem for certName, valid until notAfter, as certbot
// leaves it in the lineage.
func writeHookCert(t *testing.T, p paths.Paths, certName string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "example.com"}, NotBefore: notAfter.AddDate(0, -3, 0), NotAfter: notAfter}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	path := p.LetsEncryptCert(certName)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil {
		t.Fatal(err)
	}
}

func runHook(t *testing.T, d sysexec.Deps, value string) (int, string) {
	t.Helper()
	var stderr bytes.Buffer
	code := certDeployed(context.Background(), d, lineage(value), audit.Writer{Paths: d.Paths}, &stderr)
	return code, stderr.String()
}

func hookCommands(run *sysexec.FakeRunner) []string {
	var lines []string
	for _, c := range run.Calls() {
		lines = append(lines, strings.Join(append([]string{c.Path}, c.Args...), " "))
	}
	return lines
}

// auditLines decodes every line of the audit log.
func auditLines(t *testing.T, p paths.Paths) []map[string]any {
	t.Helper()
	b, err := os.ReadFile(p.AuditLog())
	if err != nil {
		t.Fatalf("the audit log: %v", err)
	}
	var lines []map[string]any
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatalf("audit line %q: %v", l, err)
		}
		lines = append(lines, m)
	}
	return lines
}

func readRecord(t *testing.T, p paths.Paths, name string) acme.Record {
	t.Helper()
	b, err := os.ReadFile(p.CertRecord(name))
	if err != nil {
		t.Fatalf("the expiry record: %v", err)
	}
	var rec acme.Record
	if err := json.Unmarshal(b, &rec); err != nil {
		t.Fatal(err)
	}
	return rec
}

func TestHookPanelLineage(t *testing.T) {
	run := sysexec.NewFakeRunner(hookNginxTest, hookReload)
	d := hookDeps(t, run)
	writeHookCert(t, d.Paths, "zimam-panel")
	if code, stderr := runHook(t, d, "/etc/letsencrypt/live/zimam-panel"); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	want := []string{"/usr/sbin/nginx -t", "/usr/bin/systemctl reload-or-restart nginx"}
	if got := hookCommands(run); !slices.Equal(got, want) {
		t.Errorf("ran %q, want %q", got, want)
	}
	if rec := readRecord(t, d.Paths, "panel"); rec.Name != "panel" || !rec.Expiry.Equal(notAfter) {
		t.Errorf("record = %+v, want panel until %v", rec, notAfter)
	}
}

func TestHookSiteLineage(t *testing.T) {
	run := sysexec.NewFakeRunner(hookNginxTest, hookReload)
	d := hookDeps(t, run)
	if err := os.WriteFile(d.Paths.SiteState("shop"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeHookCert(t, d.Paths, "zimam-shop")
	if code, stderr := runHook(t, d, "/etc/letsencrypt/live/zimam-shop"); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if rec := readRecord(t, d.Paths, "shop"); rec.Name != "shop" || !rec.Expiry.Equal(notAfter) {
		t.Errorf("record = %+v, want shop until %v", rec, notAfter)
	}
}

func TestHookRejectsUnknownLineage(t *testing.T) {
	for _, value := range []string{
		"/etc/letsencrypt/live/other",
		"/etc/letsencrypt/live/zimam-../x",
		"",
		"/etc/letsencrypt/live/zimam-panel/",
		"/etc/letsencrypt/live/zimam-panel/../x",
		"/etc/letsencrypt/live/zimam-panel\n",
		"/var/../etc/letsencrypt/live/zimam-panel",
		"/etc/letsencrypt/live/zimam-blog", // a valid name with no state file
		"/etc/letsencrypt/live/zimam-link", // a state file that is a symlink
	} {
		run := sysexec.NewFakeRunner()
		d := hookDeps(t, run)
		if err := os.Symlink("/etc/passwd", d.Paths.SiteState("link")); err != nil {
			t.Fatal(err)
		}
		writeHookCert(t, d.Paths, "zimam-panel")
		code, stderr := runHook(t, d, value)
		if code != 1 {
			t.Errorf("%q: exit %d, want 1", value, code)
		}
		if !strings.Contains(stderr, "RENEWED_LINEAGE") {
			t.Errorf("%q: stderr %q doesn't say what was refused", value, stderr)
		}
		if got := hookCommands(run); len(got) != 0 {
			t.Errorf("%q: ran %q", value, got)
		}
		entries, _ := os.ReadDir(d.Paths.CertsDir())
		if len(entries) != 0 {
			t.Errorf("%q: wrote %d expiry records", value, len(entries))
		}
		if lines := auditLines(t, d.Paths); len(lines) != 1 || lines[0]["outcome"] != "error:validation_failed" {
			t.Errorf("%q: audit %v, want one error:validation_failed entry", value, lines)
		}
	}
}

func TestHookAudited(t *testing.T) {
	run := sysexec.NewFakeRunner(hookNginxTest, hookReload)
	d := hookDeps(t, run)
	writeHookCert(t, d.Paths, "zimam-panel")
	if code, stderr := runHook(t, d, "/etc/letsencrypt/live/zimam-panel"); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	lines := auditLines(t, d.Paths)
	if len(lines) != 1 {
		t.Fatalf("%d audit entries, want 1", len(lines))
	}
	e := lines[0]
	params, _ := e["params"].(map[string]any)
	if e["actor"] != "cli:root" || e["peer_uid"] != nil || e["command"] != "hook cert-deployed" || e["outcome"] != "ok" ||
		params["lineage"] != "/etc/letsencrypt/live/zimam-panel" || e["ts"] == nil {
		t.Errorf("audit entry %v", e)
	}
}

func TestHookFailedNginxTest(t *testing.T) {
	bad := hookNginxTest
	bad.Result = sysexec.Result{ExitCode: 1, Stderr: []byte("nginx: [emerg] cannot load certificate")}
	run := sysexec.NewFakeRunner(bad, hookNginxTest)
	d := hookDeps(t, run)
	writeHookCert(t, d.Paths, "zimam-panel")
	code, stderr := runHook(t, d, "/etc/letsencrypt/live/zimam-panel")
	if code != 1 || !strings.Contains(stderr, "cannot load certificate") {
		t.Errorf("exit %d, stderr %q; want 1 with nginx's output", code, stderr)
	}
	if slices.Contains(hookCommands(run), "/usr/bin/systemctl reload-or-restart nginx") {
		t.Error("reloaded Nginx after a failed test")
	}
	if _, err := os.Stat(d.Paths.CertRecord("panel")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("wrote the expiry record (Stat: %v)", err)
	}
	if lines := auditLines(t, d.Paths); len(lines) != 1 || lines[0]["outcome"] != "error:internal" {
		t.Errorf("audit %v, want one error:internal entry", lines)
	}
}

func TestHookUsage(t *testing.T) {
	code, _, stderr := runCapture([]string{"hook", "cert-deployed", "extra"})
	if code != 2 || !strings.Contains(stderr, "usage") {
		t.Errorf("exit %d, stderr %q; want 2 and usage", code, stderr)
	}
}
