//go:build roottest

package valkey

import (
	"bufio"
	"context"
	"crypto/sha256"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	"zimam/agent/internal/metrics"
	"zimam/agent/internal/paths"
	"zimam/agent/internal/services"
	"zimam/agent/internal/sysexec"
)

// This test installs Valkey from the archive onto the machine it runs on, so
// it runs only where that is wanted: a throwaway container, with
// ZIMAM_ROOTTEST_VALKEY=1, as go test -tags roottest ./internal/valkey.

func rootDeps(t *testing.T) sysexec.Deps {
	t.Helper()
	if os.Getenv("ZIMAM_ROOTTEST_VALKEY") != "1" {
		t.Skip("set ZIMAM_ROOTTEST_VALKEY=1 to install Valkey on this machine")
	}
	if os.Geteuid() != 0 {
		t.Fatal("the roottest tests need root")
	}
	return sysexec.Deps{Paths: paths.Paths{}, Run: sysexec.ExecRunner{}, Logf: t.Logf, Now: time.Now}
}

// listeners returns the local addresses, in hex, of the TCP sockets that
// listen on port 6379, from /proc/net/tcp and /proc/net/tcp6.
func listeners(t *testing.T) (v4, v6 []string) {
	t.Helper()
	for file, dst := range map[string]*[]string{"/proc/net/tcp": &v4, "/proc/net/tcp6": &v6} {
		f, err := os.Open(file)
		if err != nil {
			t.Fatal(err)
		}
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			fields := strings.Fields(sc.Text())
			if len(fields) < 4 || fields[3] != "0A" { // 0A is LISTEN
				continue
			}
			if addr, port, _ := strings.Cut(fields[1], ":"); port == "18EB" { // 6379
				*dst = append(*dst, addr)
			}
		}
		f.Close()
	}
	return v4, v6
}

func shell(t *testing.T, name string, args ...string) string {
	t.Helper()
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v: %v\n%s", name, args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func mainPID(t *testing.T) string {
	t.Helper()
	return shell(t, "/usr/bin/systemctl", "show", "-p", "MainPID", "--value", unit)
}

func isActive() bool {
	return exec.Command("/usr/bin/systemctl", "is-active", "--quiet", unit).Run() == nil
}

// asUser runs valkey-cli as a user with a password in the environment, as a
// site's client would, and returns what it printed.
func asUser(user, pass string, args ...string) string {
	cmd := exec.Command("/usr/bin/valkey-cli", append([]string{"--no-auth-warning", "--user", user, "-h", "127.0.0.1", "-p", "6379"}, args...)...)
	cmd.Env = append(os.Environ(), "REDISCLI_AUTH="+pass)
	out, _ := cmd.CombinedOutput()
	return strings.TrimSpace(string(out))
}

// psWatcher samples the process list for a secret until stopped.
type psWatcher struct {
	stop    chan struct{}
	wg      sync.WaitGroup
	samples int
	hits    int
}

func watchPS(secret string) *psWatcher {
	w := &psWatcher{stop: make(chan struct{})}
	w.wg.Add(1)
	go func() {
		defer w.wg.Done()
		for {
			select {
			case <-w.stop:
				return
			default:
			}
			out, _ := exec.Command("/usr/bin/ps", "-eo", "args").Output()
			w.samples++
			if strings.Contains(string(out), secret) {
				w.hits++
			}
		}
	}()
	return w
}

func (w *psWatcher) finish() (samples, hits int) {
	close(w.stop)
	w.wg.Wait()
	return w.samples, w.hits
}

func TestRootValkeyInstall(t *testing.T) {
	d := rootDeps(t)
	ctx := context.Background()

	version, err := Install(ctx, d, noUsers)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	t.Logf("installed Valkey %s", version)
	st, err := services.Load(d.Paths)
	if err != nil || st.Valkey != (services.Entry{Installed: true, Version: version}) {
		t.Fatalf("services.json: %+v, %v", st, err)
	}

	// Owners and modes.
	for path, want := range map[string]string{
		d.Paths.ValkeyDir():       "root:valkey 750",
		d.Paths.ValkeyConf():      "root:valkey 640",
		d.Paths.ValkeyACL():       "root:valkey 640",
		d.Paths.ValkeyAdminPass(): "root:root 600",
		d.Paths.ValkeyDropIn():    "root:root 644",
	} {
		if got := shell(t, "/usr/bin/stat", "-c", "%U:%G %a", path); got != want {
			t.Errorf("%s is %s, want %s", path, got, want)
		}
	}

	// Only loopback listens.
	v4, v6 := listeners(t)
	if len(v4) != 1 || v4[0] != "0100007F" {
		t.Errorf("port 6379 listens on %v over IPv4, want only 127.0.0.1", v4)
	}
	if len(v6) != 1 || v6[0] != "00000000000000000000000001000000" {
		t.Errorf("port 6379 listens on %v over IPv6, want only ::1", v6)
	}

	// default is off: no anonymous use, and its login is refused.
	if out := shell(t, "/usr/bin/valkey-cli", "-h", "127.0.0.1", "PING"); !strings.HasPrefix(out, "NOAUTH") {
		t.Errorf("an anonymous PING was answered %q", out)
	}
	if out := asUser("default", "anything", "PING"); !strings.Contains(out, "WRONGPASS") {
		t.Errorf("default logged in or gave %q, want WRONGPASS", out)
	}
	pass := strings.TrimSpace(readFile(t, d.Paths.ValkeyAdminPass()))
	if out := asUser("zimam-admin", "not the password", "PING"); !strings.Contains(out, "WRONGPASS") {
		t.Errorf("the admin with a wrong password gave %q", out)
	}
	if out, err := Cli(ctx, d, "PING"); err != nil || out != "PONG" {
		t.Errorf("the admin's PING = %q, %v", out, err)
	}
	if !isActive() {
		t.Fatal("valkey-server is not active")
	}
	if out := shell(t, "/usr/bin/systemctl", "show", "-p", "ExecStart", "--value", unit); !strings.Contains(out, "/etc/zimam/valkey/valkey.conf") {
		t.Errorf("ExecStart = %s, want the unit to run Zimam's config", out)
	}

	// A re-run converges: no restart, the same admin password, and the
	// password never in a process list while valkey-cli runs.
	pid := mainPID(t)
	w := watchPS(pass)
	if _, err := Install(ctx, d, noUsers); err != nil {
		t.Fatalf("second Install: %v", err)
	}
	for range 40 {
		if _, err := Cli(ctx, d, "PING"); err != nil {
			t.Fatal(err)
		}
	}
	samples, hits := w.finish()
	t.Logf("the process list was sampled %d times during the re-run and 40 valkey-cli runs", samples)
	if hits > 0 {
		t.Errorf("%d samples of the process list showed the admin password", hits)
	}
	if now := mainPID(t); now != pid {
		t.Errorf("Valkey restarted on a re-run that changed nothing: pid %s, then %s", pid, now)
	}
	if again := strings.TrimSpace(readFile(t, d.Paths.ValkeyAdminPass())); again != pass {
		t.Error("the re-run changed the admin password")
	}

	// Bad files are refused: the previous files come back and the server
	// runs on.
	conf, acl, dropIn := readFile(t, d.Paths.ValkeyConf()), readFile(t, d.Paths.ValkeyACL()), readFile(t, d.Paths.ValkeyDropIn())
	uid, gid, err := serviceIDs(d.Paths)
	if err != nil {
		t.Fatal(err)
	}
	total, err := metrics.MemTotal(d.Paths)
	if err != nil {
		t.Fatal(err)
	}
	memMiB := MaxMemoryMiB(total)
	with := func(c, a string) files { return files{conf: []byte(c), acl: []byte(a), dropIn: []byte(dropIn)} }
	for name, bad := range map[string]files{
		"an unknown directive":      with(conf+"not-a-directive yes\n", acl),
		"a memory limit that isn't": with(strings.Replace(conf, "mb\n", "lots\n", 1), acl),
		"an ACL with a bad hash":    with(conf, strings.Replace(acl, "#", "#abc", 1)),
		"an ACL rule that is none":  with(conf, acl+"user s_x on nopass +@nosuchcategory\n"),
		"an ACL that isn't an ACL":  with(conf, "garbage here\n"),
		// These two start: the restart happens and the live values refuse them.
		"listening on every address": with(strings.Replace(conf, "bind 127.0.0.1 -::1", "bind * -::*", 1), acl),
		"the default user on":        with(conf, strings.Replace(acl, "user default off", "user default on nopass ~* &* +@all", 1)),
	} {
		before := mainPID(t)
		_, err := applyFiles(ctx, d, bad, uid, gid, memMiB)
		t.Logf("%s: %v", name, err)
		if err == nil {
			t.Errorf("%s: applyFiles accepted it", name)
		}
		if readFile(t, d.Paths.ValkeyConf()) != conf || readFile(t, d.Paths.ValkeyACL()) != acl || readFile(t, d.Paths.ValkeyDropIn()) != dropIn {
			t.Errorf("%s: the previous files are not back", name)
		}
		if !isActive() {
			t.Fatalf("%s: Valkey is not running after the rollback", name)
		}
		if out, err := Cli(ctx, d, "PING"); err != nil || out != "PONG" {
			t.Fatalf("%s: the admin's PING = %q, %v", name, out, err)
		}
		if v4, v6 := listeners(t); len(v4) != 1 || v4[0] != "0100007F" || len(v6) != 1 {
			t.Errorf("%s: port 6379 listens on %v and %v", name, v4, v6)
		}
		t.Logf("%s: pid %s, then %s", name, before, mainPID(t))
		if out := shell(t, "/usr/bin/valkey-cli", "-h", "127.0.0.1", "PING"); !strings.HasPrefix(out, "NOAUTH") {
			t.Errorf("%s: an anonymous PING was answered %q", name, out)
		}
	}

	// A site's user is kept by a re-run and is confined to its prefix.
	sitePass := "siteSitePassword0123456789abcdefghijklm"
	withSite := func() ([]SiteUser, error) {
		return []SiteUser{{Name: "blog", Hash: sha256.Sum256([]byte(sitePass))}}, nil
	}
	if _, err := Install(ctx, d, withSite); err != nil {
		t.Fatalf("Install with a site user: %v", err)
	}
	if out := asUser("s_blog", sitePass, "SET", "s_blog:k", "1"); out != "OK" {
		t.Errorf("SET in the site's prefix = %q", out)
	}
	if out := asUser("s_blog", sitePass, "GET", "other:k"); !strings.Contains(out, "NOPERM") {
		t.Errorf("GET outside the prefix = %q, want NOPERM", out)
	}
	if out := asUser("s_blog", sitePass, "FLUSHDB"); !strings.Contains(out, "NOPERM") {
		t.Errorf("FLUSHDB = %q, want NOPERM", out)
	}
	pid = mainPID(t)
	if _, err := Install(ctx, d, withSite); err != nil {
		t.Fatalf("Install again with the site user: %v", err)
	}
	if now := mainPID(t); now != pid {
		t.Errorf("Valkey restarted on a re-run with the same users: pid %s, then %s", pid, now)
	}

	// Nothing of the candidate server is left behind.
	if _, err := os.Stat(d.Paths.ValkeyTestDir()); err == nil {
		t.Errorf("%s is still there", d.Paths.ValkeyTestDir())
	}
	if out, _ := exec.Command("/usr/bin/pgrep", "-af", "zimam-valkey-test").CombinedOutput(); strings.TrimSpace(string(out)) != "" {
		t.Errorf("a candidate server is still running:\n%s", out)
	}
	if entries, _ := os.ReadDir(d.Paths.ValkeyDir()); len(entries) != 3 {
		t.Errorf("%s holds %d entries, want valkey.conf, users.acl and admin.pass", d.Paths.ValkeyDir(), len(entries))
	}
}
