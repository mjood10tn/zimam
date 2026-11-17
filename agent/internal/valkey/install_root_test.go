//go:build roottest

package valkey

import (
	"bufio"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"zimam/agent/contract"
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

	// From before the package is installed to the end, nobody may get a reply to
	// an unauthenticated PING: the package server on its own settings would let
	// any local user in, and so would a candidate with the default user on, which
	// is refused before the real server is touched.
	anon := watchAnonymous()
	defer func() {
		samples, pongs, errs := anon.finish()
		t.Logf("unauthenticated PINGs: %d samples, %d refused, %d with no server, %d answered", samples, samples-pongs-errs, errs, pongs)
		if pongs > 0 {
			t.Errorf("%d unauthenticated PINGs were answered with PONG", pongs)
		}
		if samples < 50 {
			t.Errorf("only %d samples were taken", samples)
		}
	}()

	// Data from before Zimam managed Valkey is refused, with the service
	// masked and nothing listening.
	if err := os.MkdirAll(d.Paths.ValkeyDataDir(), 0o750); err != nil {
		t.Fatal(err)
	}
	planted := filepath.Join(d.Paths.ValkeyDataDir(), "dump.rdb")
	if err := os.WriteFile(planted, []byte("REDIS0012"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Install(ctx, d, noUsers)
	var ae *contract.AgentError
	if !errors.As(err, &ae) || ae.Code != contract.ErrConflict {
		t.Fatalf("Install over a dump = %v, want a conflict", err)
	}
	t.Logf("refused: %v", err)
	if got := shell(t, "/usr/bin/dpkg-query", "-W", "-f=${db:Status-Abbrev}", packageName); got != "ii" {
		t.Errorf("with the unit masked, the package is %q, want ii (installed)", got)
	}
	assertHeld(t, d)
	if _, err := os.Stat(planted); err != nil {
		t.Errorf("the planted dump is gone: %v", err)
	}
	if _, err := os.Stat("/var/log/valkey/valkey-server.log"); err == nil {
		t.Error("a server ran and logged, though the unit was masked")
	}
	for _, p := range []string{d.Paths.ValkeyConf(), d.Paths.ValkeyACL(), d.Paths.ValkeyDropIn(), d.Paths.ServicesState()} {
		if _, err := os.Stat(p); err == nil {
			t.Errorf("%s was written", p)
		}
	}
	if err := os.Remove(planted); err != nil {
		t.Fatal(err)
	}

	// A first install that fails at the start leaves the service stopped and
	// masked, never the package's settings running.
	breakIn := filepath.Join(d.Paths.ValkeyDropInDir(), "99-break.conf")
	if err := os.MkdirAll(d.Paths.ValkeyDropInDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(breakIn, []byte("[Service]\nExecStartPre=/bin/false\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Install(ctx, d, noUsers); err == nil {
		t.Fatal("a first install whose service can't start succeeded")
	} else {
		t.Logf("failed as it should: %v", err)
	}
	assertHeld(t, d)
	for _, p := range []string{d.Paths.ValkeyConf(), d.Paths.ValkeyACL(), d.Paths.ValkeyDropIn(), d.Paths.ValkeyAdminPass(), d.Paths.ServicesState(), planted} {
		if _, err := os.Stat(p); err == nil {
			t.Errorf("%s is there after a failed first install", p)
		}
	}
	if err := os.Remove(breakIn); err != nil {
		t.Fatal(err)
	}

	// The next run continues from there.
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
	if out, err := cliAdmin(ctx, d, "PING"); err != nil || out != "PONG" {
		t.Errorf("the admin's PING = %q, %v", out, err)
	}
	if !isActive() {
		t.Fatal("valkey-server is not active")
	}
	if out := shell(t, "/usr/bin/systemctl", "is-enabled", unit); out != "enabled" {
		t.Errorf("the service is %s after the install, want enabled", out)
	}
	if out := shell(t, "/usr/bin/dpkg-query", "-W", "-f=${db:Status-Abbrev}", packageName); out != "ii" {
		t.Errorf("the package is %q, want ii", out)
	}
	if out := shell(t, "/usr/bin/systemctl", "show", "-p", "ExecStart", "--value", unit); !strings.Contains(out, "/etc/zimam/valkey/valkey.conf") {
		t.Errorf("ExecStart = %s, want the unit to run Zimam's config", out)
	}
	if out := shell(t, "/usr/bin/systemctl", "show", "-p", "ExecStartPre", "--value", unit); strings.Contains(out, "false") {
		t.Errorf("the break-in drop-in is still read: %s", out)
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
		if _, err := cliAdmin(ctx, d, "PING"); err != nil {
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
		// The candidate server shows these two before the real server is touched, so
		// the default user is never on where anyone could reach it.
		"listening on every address": with(strings.Replace(conf, "bind 127.0.0.1 -::1", "bind * -::*", 1), acl),
		"the default user on":        with(conf, strings.Replace(acl, "user default off", "user default on nopass ~* &* +@all", 1)),
		// This one starts: the real server restarts, and its live values refuse it.
		"a memory limit other than the one asked for": with(strings.Replace(conf, fmt.Sprintf("maxmemory %dmb", memMiB), "maxmemory 100mb", 1), acl),
	} {
		before := mainPID(t)
		_, err := applyFiles(ctx, d, bad, target{uid: uid, gid: gid, memMiB: memMiB})
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
		if out, err := cliAdmin(ctx, d, "PING"); err != nil || out != "PONG" {
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

	// A working server whose settings lose a file is stopped and masked, and
	// brought back on Zimam's settings by the next run, with the sites' data
	// kept and nobody let in meanwhile; and one left masked by hand is healed.
	if out, err := cliAdmin(ctx, d, "SET", "s_blog:kept", "the sites' own data"); err != nil || out != "OK" {
		t.Fatalf("SET as the admin = %q, %v", out, err)
	}
	if err := os.Remove(d.Paths.ValkeyDropIn()); err != nil {
		t.Fatal(err)
	}
	if _, err := Install(ctx, d, noUsers); err != nil {
		t.Fatalf("Install after the drop-in was removed: %v", err)
	}
	assertRunning(t, d, "the drop-in was made again")
	if out, err := cliAdmin(ctx, d, "GET", "s_blog:kept"); err != nil || out != "the sites' own data" {
		t.Errorf("the key after the run that made the drop-in again = %q, %v", out, err)
	}
	shell(t, "/usr/bin/systemctl", "mask", "--now", unit)
	if isActive() {
		t.Fatal("the service is active after mask --now")
	}
	if _, err := Install(ctx, d, noUsers); err != nil {
		t.Fatalf("Install after the service was masked by hand: %v", err)
	}
	assertRunning(t, d, "the masked service was healed")
	if out, err := cliAdmin(ctx, d, "GET", "s_blog:kept"); err != nil || out != "the sites' own data" {
		t.Errorf("the key after the run that unmasked the service = %q, %v", out, err)
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

	// A candidate server that a killed job left running is ended by the next
	// test of the files.
	leftover := startLeftoverCandidate(t, d)
	if err := testCandidate(ctx, d, uid, gid, aclUsers([]byte(readFile(t, d.Paths.ValkeyACL())))); err != nil {
		t.Fatalf("testCandidate: %v", err)
	}
	if !leftover.gone() {
		t.Error("the leftover candidate server is still running")
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

// assertHeld checks that the service is masked and stopped, that nothing
// listens on 6379 and that no valkey-server runs.
func assertHeld(t *testing.T, d sysexec.Deps) {
	t.Helper()
	if out, _ := exec.Command("/usr/bin/systemctl", "is-enabled", unit).Output(); strings.TrimSpace(string(out)) != "masked" {
		t.Errorf("the service is %q, want masked", strings.TrimSpace(string(out)))
	}
	if isActive() {
		t.Error("the service is active")
	}
	if v4, v6 := listeners(t); len(v4)+len(v6) != 0 {
		t.Errorf("port 6379 listens on %v and %v", v4, v6)
	}
	if out, _ := exec.Command("/usr/bin/pgrep", "-x", "valkey-server").Output(); strings.TrimSpace(string(out)) != "" {
		t.Errorf("a valkey-server runs: %s", out)
	}
}

// anonymousWatcher asks 127.0.0.1:6379 PING without credentials in a loop.
type anonymousWatcher struct {
	stop                  chan struct{}
	wg                    sync.WaitGroup
	samples, pongs, noSrv int
}

func watchAnonymous() *anonymousWatcher {
	w := &anonymousWatcher{stop: make(chan struct{})}
	w.wg.Add(1)
	go func() {
		defer w.wg.Done()
		for {
			select {
			case <-w.stop:
				return
			default:
			}
			out, _ := exec.Command("/usr/bin/valkey-cli", "-e", "-h", "127.0.0.1", "-p", "6379", "PING").CombinedOutput()
			w.samples++
			switch s := string(out); {
			case strings.Contains(s, "PONG"):
				w.pongs++
			case strings.Contains(s, "Could not connect") || strings.Contains(s, "Connection refused"):
				w.noSrv++
			}
		}
	}()
	return w
}

// finish stops the watcher and returns how many samples it took, how many
// were answered PONG, and how many found no server.
func (w *anonymousWatcher) finish() (samples, pongs, noServer int) {
	close(w.stop)
	w.wg.Wait()
	return w.samples, w.pongs, w.noSrv
}

// leftoverCandidate is a candidate server as a killed job leaves it.
type leftoverCandidate struct{ pid int }

func startLeftoverCandidate(t *testing.T, d sysexec.Deps) leftoverCandidate {
	t.Helper()
	p := d.Paths
	uid, gid, err := serviceIDs(p)
	if err != nil {
		t.Fatal(err)
	}
	dir := p.ValkeyTestDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chown(dir, uid, gid); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(runuser, "-u", serviceUser, "--", valkeyServer, p.ValkeyConf(), "--aclfile", p.ValkeyACL(),
		"--port", "0", "--unixsocket", filepath.Join(dir, "s.sock"), "--daemonize", "no", "--supervised", "no",
		"--pidfile", filepath.Join(dir, "p"), "--logfile", "", "--dir", dir, "--save", "", "--appendonly", "no")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go cmd.Wait()
	for range 100 {
		if b, err := os.ReadFile(filepath.Join(dir, "p")); err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil {
				return leftoverCandidate{pid}
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("the leftover candidate never wrote its pidfile")
	return leftoverCandidate{}
}

func (l leftoverCandidate) gone() bool {
	for range 40 {
		if _, err := os.Stat("/proc/" + strconv.Itoa(l.pid)); err != nil {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return false
}

// assertRunning checks that the service is enabled and active on Zimam's
// config, with the admin logged in and nobody else.
func assertRunning(t *testing.T, d sysexec.Deps, what string) {
	t.Helper()
	if !isActive() {
		t.Errorf("%s: the service is not active", what)
	}
	if out, _ := exec.Command("/usr/bin/systemctl", "is-enabled", unit).Output(); strings.TrimSpace(string(out)) != "enabled" {
		t.Errorf("%s: the service is %q, want enabled", what, strings.TrimSpace(string(out)))
	}
	if out := shell(t, "/usr/bin/systemctl", "show", "-p", "ExecStart", "--value", unit); !strings.Contains(out, "/etc/zimam/valkey/valkey.conf") {
		t.Errorf("%s: ExecStart = %s", what, out)
	}
	if out, err := cliAdmin(context.Background(), d, "PING"); err != nil || out != "PONG" {
		t.Errorf("%s: the admin's PING = %q, %v", what, out, err)
	}
}
