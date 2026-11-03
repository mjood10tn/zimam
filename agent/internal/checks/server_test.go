package checks

import (
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
	"strings"
	"testing"
	"time"

	"zimam/agent/contract"
	"zimam/agent/internal/paths"
	"zimam/agent/internal/sysexec"
)

func writeTestCert(t *testing.T, path string, notAfter time.Time) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "shop.example.com"}, NotBefore: notAfter.AddDate(0, 0, -90), NotAfter: notAfter}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	write(t, path, string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})))
}

func serverEnv(t *testing.T, scripts ...sysexec.Script) Env {
	t.Helper()
	p := paths.Paths{Root: t.TempDir()}
	return Env{D: sysexec.Deps{Paths: p, Run: sysexec.NewFakeRunner(scripts...), Logf: t.Logf, Now: func() time.Time { return testNow }}}
}

const ufwActive = `Status: active

To                         Action      From
--                         ------      ----
22/tcp                     ALLOW       Anywhere
80/tcp                     ALLOW       Anywhere
443/tcp                    ALLOW       Anywhere
22/tcp (v6)                ALLOW       Anywhere (v6)
`

func TestFirewallActive(t *testing.T) {
	ufw := func(out string) sysexec.Script {
		return sysexec.Script{Path: "/usr/sbin/ufw", Args: []string{"status"}, Result: sysexec.Result{Stdout: []byte(out)}}
	}
	e := serverEnv(t, ufw(ufwActive))
	write(t, e.D.Paths.ProcNetTCP(), "  sl  local_address rem_address   st\n   0: 00000000:0016 00000000:0000 0A 0\n   1: 0100007F:0CEA 00000000:0000 0A 0\n   2: 0100007F:0050 0200007F:9999 01 0\n")
	write(t, e.D.Paths.ProcNetTCP6(), "  sl  local_address rem_address   st\n   0: 00000000000000000000000000000000:01BB 00000000000000000000000000000000:0000 0A 0\n")
	f := firewallActive(context.Background(), e)
	wantStatus(t, f, "firewall_active", contract.FindingPass)
	for _, want := range []string{"22/tcp, 80/tcp, 443/tcp", "22, 443, 3306"} {
		if !strings.Contains(f.Detail, want) {
			t.Errorf("detail %q lacks %q", f.Detail, want)
		}
	}
	e = serverEnv(t, ufw("Status: inactive\n"))
	wantStatus(t, firewallActive(context.Background(), e), "firewall_active", contract.FindingFail)
}

func TestSshChecks(t *testing.T) {
	sshd := func(out string) sysexec.Script {
		return sysexec.Script{Path: "/usr/sbin/sshd", Args: []string{"-T"}, Result: sysexec.Result{Stdout: []byte(out)}}
	}
	for _, tc := range []struct {
		out            string
		password, root contract.FindingStatus
	}{
		{"port 22\npasswordauthentication no\npermitrootlogin prohibit-password\n", contract.FindingPass, contract.FindingPass},
		{"passwordauthentication yes\npermitrootlogin yes\n", contract.FindingWarn, contract.FindingWarn},
		{"passwordauthentication no\npermitrootlogin no\n", contract.FindingPass, contract.FindingPass},
		{"permitrootlogin without-password\n", contract.FindingWarn, contract.FindingPass},
	} {
		got := sshChecks(context.Background(), serverEnv(t, sshd(tc.out)))
		wantStatus(t, got[0], "ssh_password_login", tc.password)
		wantStatus(t, got[1], "ssh_root_login", tc.root)
	}
	failing := sysexec.Script{Path: "/usr/sbin/sshd", Args: []string{"-T"}, Result: sysexec.Result{ExitCode: 255}}
	got := sshChecks(context.Background(), serverEnv(t, failing))
	wantStatus(t, got[0], "ssh_password_login", contract.FindingInconclusive)
	wantStatus(t, got[1], "ssh_root_login", contract.FindingInconclusive)
}

func TestUpdatesAndReboot(t *testing.T) {
	e := serverEnv(t)
	wantStatus(t, securityUpdates(e), "security_updates_pending", contract.FindingInconclusive)
	write(t, e.D.Paths.UpdatesCache(), `{"total":4,"security":2,"checked_at":"2026-11-03T08:00:00Z"}`)
	wantStatus(t, securityUpdates(e), "security_updates_pending", contract.FindingWarn)
	write(t, e.D.Paths.UpdatesCache(), `{"total":4,"security":0,"checked_at":"2026-11-03T08:00:00Z"}`)
	wantStatus(t, securityUpdates(e), "security_updates_pending", contract.FindingPass)

	wantStatus(t, rebootRequired(e), "reboot_required", contract.FindingPass)
	write(t, e.D.Paths.RebootRequired(), "*** System restart required ***\n")
	wantStatus(t, rebootRequired(e), "reboot_required", contract.FindingWarn)
}

func TestStockPoolsRemoved(t *testing.T) {
	e := serverEnv(t)
	for _, v := range []contract.PHPVersion{"8.4", "8.5"} {
		write(t, e.D.Paths.PHPFPMBinary(v), "")
	}
	write(t, e.D.Paths.PanelPool("8.5"), "")
	wantStatus(t, stockPools(e), "stock_www_pools_removed", contract.FindingPass)
	write(t, e.D.Paths.PHPWwwPool("8.4"), "[www]\n")
	f := stockPools(e)
	wantStatus(t, f, "stock_www_pools_removed", contract.FindingFail)
	if !strings.Contains(f.Detail, "8.4") || strings.Contains(f.Detail, "8.5") {
		t.Errorf("detail = %q", f.Detail)
	}
}

// The socket is root:zimam-agent 0660 and zimam-agent has no member but
// zimam (§5.1, §9.7). An extra member fails.
func TestAgentSocketPermissions(t *testing.T) {
	e := serverEnv(t)
	gid := os.Getgid()
	sock := e.D.Paths.Socket()
	if err := os.MkdirAll(filepath.Dir(sock), 0o755); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	os.Chmod(sock, 0o660)
	group := func(members string) {
		write(t, e.D.Paths.EtcGroup(), "root:x:0:\nzimam-agent:x:"+itoa(gid)+":"+members+"\nzimam:x:999:www-data\n")
	}
	// Unit tests don't run as root, so the owner they expect is their own.
	socketOwner = os.Getuid()
	t.Cleanup(func() { socketOwner = 0 })

	group("zimam")
	wantStatus(t, socketPermissions(e), "agent_socket_permissions", contract.FindingPass)
	group("zimam,mallory")
	f := socketPermissions(e)
	wantStatus(t, f, "agent_socket_permissions", contract.FindingFail)
	if !strings.Contains(f.Detail, "mallory") {
		t.Errorf("detail = %q", f.Detail)
	}
	group("zimam")
	os.Chmod(sock, 0o666)
	wantStatus(t, socketPermissions(e), "agent_socket_permissions", contract.FindingFail)
}

func itoa(n int) string {
	return strings.TrimSpace(strings.Repeat(" ", 0) + big.NewInt(int64(n)).String())
}
