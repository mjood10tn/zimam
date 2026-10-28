//go:build roottest

package helper

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// These tests need root and a real user: the e2e container runs them
// (Task 24) with go test -tags roottest ./internal/helper.

const rootTestUser = "s_zimamhelpertest"

// rootSetup creates rootTestUser and returns its account and a client that
// runs this test binary as the helper (TestMain), with the real credential
// switch.
func rootSetup(t *testing.T) (Account, *Client) {
	t.Helper()
	if os.Geteuid() != 0 {
		t.Fatal("the roottest tests need root")
	}
	exec.Command("/usr/sbin/userdel", "--remove", rootTestUser).Run() // left over from an earlier run
	out, err := exec.Command("/usr/sbin/useradd", "--create-home", "--user-group", "--shell", "/usr/sbin/nologin", rootTestUser).CombinedOutput()
	if err != nil {
		t.Fatalf("useradd: %v\n%s", err, out)
	}
	t.Cleanup(func() { exec.Command("/usr/sbin/userdel", "--remove", rootTestUser).Run() })
	a, err := LookupAccount(rootTestUser)
	if err != nil {
		t.Fatal(err)
	}
	// go test keeps its binary in a 0700 directory, out of the user's reach.
	dir, err := os.MkdirTemp("", "zimam-helper-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "helper.test")
	copyFile(t, bin)
	return a, &Client{AgentBinary: bin}
}

func copyFile(t *testing.T, to string) {
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

func TestRootHelperRunsAsTheUser(t *testing.T) {
	a, c := rootSetup(t)
	ctx := context.Background()
	if err := c.Mkdir(ctx, a, "made", 0o750); err != nil {
		t.Fatal(err)
	}
	if err := c.Write(ctx, a, "made/file", 0o640, strings.NewReader("x")); err != nil {
		t.Fatal(err)
	}
	if err := c.SymlinkSwap(ctx, a, "current", "releases/one"); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"made", "made/file", "current"} {
		fi, err := os.Lstat(filepath.Join(a.Home, p))
		if err != nil {
			t.Fatal(err)
		}
		if st := fi.Sys().(*syscall.Stat_t); st.Uid != a.UID || st.Gid != a.GID {
			t.Errorf("%s belongs to %d:%d, want %d:%d", p, st.Uid, st.Gid, a.UID, a.GID)
		}
	}
}

func TestRootHelperCantReadRootFile(t *testing.T) {
	a, c := rootSetup(t)
	// Inside the home, so os.Root allows it: only the kernel's check stops it.
	if err := os.WriteFile(filepath.Join(a.Home, "root-secret"), []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := c.Read(context.Background(), a, "root-secret", 0, 100)
	if err == nil || len(res.Data) != 0 || !strings.Contains(err.Error(), "permission denied") {
		t.Errorf("Read(root-secret) = %q, %v; want permission denied", res.Data, err)
	}
}
