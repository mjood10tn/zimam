//go:build roottest

package deploy

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"zimam/agent/contract"
	"zimam/agent/internal/config"
	"zimam/agent/internal/helper"
	"zimam/agent/internal/paths"
	"zimam/agent/internal/sites"
	"zimam/agent/internal/sysexec"
)

// These tests need root and a real user: the e2e container runs them with
// go test -tags roottest ./internal/deploy.

// TestMain lets the tests run this test binary as the helper: started as
// "as-user ...", it serves one operation, as zimam-agent does.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "as-user" {
		ids, err := helper.ProcessIDs()
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(helper.Main(ids, os.Args[2:], os.Stdin, os.Stdout, os.Stderr))
	}
	os.Exit(m.Run())
}

const rootSite = "repotest"

// TestRootRepoMakesKeyAsSiteUser runs site.repo for real: ssh-keygen as the
// site user through the helper, the modes the site layout names, and a
// fingerprint that matches ssh-keygen's own.
func TestRootRepoMakesKeyAsSiteUser(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Fatal("the roottest tests need root")
	}
	user := "s_" + rootSite
	exec.Command("/usr/sbin/userdel", "--remove", user).Run() // left over from an earlier run
	if out, err := exec.Command("/usr/sbin/useradd", "--create-home", "--user-group", "--shell", "/usr/sbin/nologin", user).CombinedOutput(); err != nil {
		t.Fatalf("useradd: %v\n%s", err, out)
	}
	t.Cleanup(func() { exec.Command("/usr/sbin/userdel", "--remove", user).Run() })
	a, err := helper.LookupAccount(user)
	if err != nil {
		t.Fatal(err)
	}

	// go test keeps its binary in a 0700 directory, out of the user's reach.
	dir, err := os.MkdirTemp("", "zimam-deploy-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "deploy.test")
	copyExecutable(t, bin)
	h := &helper.Client{AgentBinary: bin}

	p := paths.Paths{Root: t.TempDir()}
	for _, b := range []string{p.GitBinary(), p.UnzipBinary()} {
		os.MkdirAll(filepath.Dir(b), 0o755)
		if err := os.WriteFile(b, nil, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	st := sites.State{Name: rootSite, Domain: "repotest.example.com", Type: contract.SiteStatic, TemplateVersion: 1, Security: sites.DefaultSecurity()}
	if err := sites.Save(p, st); err != nil {
		t.Fatal(err)
	}
	d := sysexec.Deps{Paths: p, Run: sysexec.NewFakeRunner(), Config: config.New("panel.example.com", "8.4", "8.5"), Logf: t.Logf, Now: time.Now}
	lookup := func(string) (helper.Account, error) { return a, nil }
	ctx := context.Background()

	res, err := Repo(ctx, d, h, lookup, contract.SiteRepoParams{Name: rootSite, Repository: "octo/app", Branch: "main"})
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]os.FileMode{".ssh": 0o700 | os.ModeDir, ".ssh/zimam_deploy": 0o600, ".ssh/known_hosts": 0o600} {
		fi, err := os.Stat(filepath.Join(a.Home, name))
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode() != want {
			t.Errorf("%s mode %v, want %v", name, fi.Mode(), want)
		}
	}
	out, err := exec.Command("/usr/bin/stat", "-c", "%U", filepath.Join(a.Home, ".ssh/zimam_deploy")).Output()
	if err != nil || strings.TrimSpace(string(out)) != user {
		t.Errorf("the key belongs to %q (%v), want %s", out, err, user)
	}
	kh, _ := os.ReadFile(filepath.Join(a.Home, ".ssh/known_hosts"))
	if string(kh) != GitHubKnownHosts {
		t.Errorf("known_hosts = %q", kh)
	}
	l, err := exec.Command("/usr/bin/ssh-keygen", "-l", "-E", "sha256", "-f", filepath.Join(a.Home, ".ssh/zimam_deploy.pub")).Output()
	if err != nil {
		t.Fatal(err)
	}
	if f := strings.Fields(string(l)); len(f) < 3 || f[1] != res.Fingerprint || f[2] != "zimam-repotest@panel.example.com" {
		t.Errorf("ssh-keygen -l = %q; result fingerprint %s", l, res.Fingerprint)
	}

	again, err := Repo(ctx, d, h, lookup, contract.SiteRepoParams{Name: rootSite, Repository: "octo/app", Branch: "main"})
	if err != nil || again.Fingerprint != res.Fingerprint {
		t.Errorf("second run: %+v, %v; want the same key", again, err)
	}
	rotated, err := Repo(ctx, d, h, lookup, contract.SiteRepoParams{Name: rootSite, Repository: "octo/app", Branch: "main", RotateKey: true})
	if err != nil || rotated.Fingerprint == res.Fingerprint {
		t.Errorf("rotation: %+v, %v; want a new key", rotated, err)
	}
}

func copyExecutable(t *testing.T, to string) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	src, err := os.Open(exe)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	dst, err := os.OpenFile(to, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o755)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(dst, src); err != nil {
		t.Fatal(err)
	}
	if err := dst.Close(); err != nil {
		t.Fatal(err)
	}
}
