package valkey

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"zimam/agent/contract"
	"zimam/agent/internal/configtx"
	"zimam/agent/internal/paths"
	"zimam/agent/internal/services"
	"zimam/agent/internal/sysexec"
)

const (
	gib         = 1 << 30
	lockTimeout = "DPkg::Lock::Timeout=300"

	// The live answers of a server that runs Zimam's files with 102 MiB of
	// memory limit (10% of 1 GiB).
	liveConfig = "bind\n127.0.0.1 -::1\nport\n6379\naclfile\n/etc/zimam/valkey/users.acl\nmaxmemory\n106954752\nmaxmemory-policy\nnoeviction\n"
	liveInfo   = "# Server\r\nredis_version:7.2.4\r\nvalkey_version:9.0.4\r\nredis_mode:standalone\r\n"
)

var (
	aptUpdate  = sysexec.Script{Path: "/usr/bin/apt-get", Args: []string{"-o", lockTimeout, "update"}}
	aptInstall = sysexec.Script{Path: "/usr/bin/apt-get", Args: []string{"-o", lockTimeout, "-o", "Dpkg::Options::=--force-confdef", "-o", "Dpkg::Options::=--force-confold",
		"install", "-y", "--no-install-recommends", "valkey-server"}}
	reload  = sysexec.Script{Path: systemctl, Args: []string{"daemon-reload"}}
	restart = sysexec.Script{Path: systemctl, Args: []string{"restart", "valkey-server"}}
	// resetFailed clears systemd's start counter before a restore's restart.
	resetFailed = sysexec.Script{Path: systemctl, Args: []string{"reset-failed", "valkey-server"}}
	// The service is held before the packages on a first install, and let go
	// only after the files passed their test.
	mask      = sysexec.Script{Path: systemctl, Args: []string{"mask", "valkey-server"}}
	unmask    = sysexec.Script{Path: systemctl, Args: []string{"unmask", "valkey-server"}}
	stop      = sysexec.Script{Path: systemctl, Args: []string{"stop", "valkey-server"}}
	enable    = sysexec.Script{Path: systemctl, Args: []string{"enable", "valkey-server"}}
	isEnabled = sysexec.Script{Path: systemctl, Args: []string{"is-enabled", "valkey-server"}, Result: sysexec.Result{Stdout: []byte("enabled\n")}}
	inactive  = sysexec.Script{Path: systemctl, Args: []string{"is-active", "--quiet", "valkey-server"}, Result: sysexec.Result{ExitCode: 3}}
)

// live is a script for a valkey-cli call to the running server as the admin.
func live(out string, args ...string) sysexec.Script {
	return sysexec.Script{Path: cliPath, Args: append([]string{"-e", "--no-auth-warning", "--user", "zimam-admin", "-h", "127.0.0.1", "-p", "6379"}, args...), Result: sysexec.Result{Stdout: []byte(out)}}
}

var (
	livePing   = live("PONG\n", "PING")
	liveCfg    = live(liveConfig, "CONFIG", "GET", "bind", "port", "aclfile", "maxmemory", "maxmemory-policy")
	liveACL    = live("user default off sanitize-payload resetchannels -@all\nuser zimam-admin on sanitize-payload #abc ~* &* +@all\n", "ACL", "LIST")
	liveServer = live(liveInfo, "INFO", "server")
)

func failing(s sysexec.Script, code int, out string) sysexec.Script {
	s.Result = sysexec.Result{ExitCode: code, Stdout: []byte(out), Stderr: []byte(out)}
	return s
}

// serverRunner is the fake runner with a candidate server in it: the
// throwaway instance makes its socket and runs until its context ends, as
// valkey-server does until it is killed. A scripted failure exits at once.
type serverRunner struct {
	*sysexec.FakeRunner
	noSocket bool // the instance runs but never makes its socket
	killed   *atomic.Int32
	onCall   func(c sysexec.Cmd, ctx context.Context) error // runs before a call is answered
}

func (r serverRunner) Run(ctx context.Context, c sysexec.Cmd) (sysexec.Result, error) {
	if r.onCall != nil {
		if err := r.onCall(c, ctx); err != nil {
			return sysexec.Result{}, err
		}
	}
	res, err := r.FakeRunner.Run(ctx, c)
	if c.Path != runuser || err != nil {
		return res, err
	}
	if !r.noSocket {
		if i := slices.Index(c.Args, "--unixsocket"); i >= 0 {
			_ = os.WriteFile(c.Args[i+1], nil, 0o600)
		}
	}
	<-ctx.Done()
	r.killed.Add(1)
	return sysexec.Result{}, ctx.Err()
}

type testEnv struct {
	d      sysexec.Deps
	run    *sysexec.FakeRunner
	srv    serverRunner
	p      paths.Paths
	logs   *strings.Builder
	killed *atomic.Int32
}

// newEnv is a server with Valkey's packages unpacked (user and group
// valkey, here the test's own so that chown works without root) and 1 GiB
// of memory, under a temporary root.
func newEnv(t *testing.T) *testEnv {
	t.Helper()
	p := paths.Paths{Root: t.TempDir()}
	for _, dir := range []string{filepath.Dir(p.ProcMeminfo()), filepath.Dir(p.EtcGroup()), p.LocksDir(), filepath.Dir(p.ValkeyTestDir())} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write := func(path, content string) {
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(p.ProcMeminfo(), fmt.Sprintf("MemTotal:       %d kB\nMemFree:         100 kB\nMemAvailable:    900 kB\nSwapTotal:       0 kB\nSwapFree:        0 kB\n", gib/1024))
	write(p.EtcGroup(), fmt.Sprintf("root:x:0:\nvalkey:x:%d:\n", os.Getgid()))
	write(p.EtcPasswd(), fmt.Sprintf("root:x:0:0:root:/root:/bin/bash\nvalkey:x:%d:%d::/var/lib/valkey:/usr/sbin/nologin\n", os.Getuid(), os.Getgid()))
	run := sysexec.NewFakeRunner()
	e := &testEnv{run: run, p: p, logs: &strings.Builder{}, killed: &atomic.Int32{}}
	e.srv = serverRunner{FakeRunner: run, killed: e.killed}
	logf := func(format string, args ...any) {
		fmt.Fprintf(e.logs, format+"\n", args...)
		t.Logf(format, args...)
	}
	e.d = sysexec.Deps{Paths: p, Run: e.srv, Logf: logf, Now: time.Now}
	return e
}

// sock is a script for a valkey-cli call to the candidate server's socket.
func (e *testEnv) sock(out string, args ...string) sysexec.Script {
	s := e.p.ValkeyTestDir() + "/s.sock"
	return sysexec.Script{Path: cliPath, Args: append([]string{"-e", "--no-auth-warning", "--user", "zimam-admin", "-s", s}, args...), Result: sysexec.Result{Stdout: []byte(out)}}
}

// the candidate server's answers: it starts, answers the admin's PING over
// its socket, and then says what it loaded: the default user off, the admin
// and these site users, bound to loopback and reading Zimam's ACL file.
func (e *testEnv) throwaway(siteUsers ...string) []sysexec.Script {
	t := e.p.ValkeyTestDir()
	args := []string{"-u", "valkey", "--", "/usr/bin/valkey-server", e.p.ValkeyConf(), "--aclfile", e.p.ValkeyACL(),
		"--port", "0", "--unixsocket", t + "/s.sock", "--daemonize", "no", "--supervised", "no", "--pidfile", t + "/p",
		"--logfile", "", "--dir", t, "--save", "", "--appendonly", "no"}
	acl := aclWith(siteUsers...).Result.Stdout
	return []sysexec.Script{
		{Path: runuser, Args: args},
		e.sock("PONG\n", "PING"),
		e.sock(string(acl), "ACL", "LIST"),
		e.sock("bind\n127.0.0.1 -::1\naclfile\n"+e.p.ValkeyACL()+"\n", "CONFIG", "GET", "bind", "aclfile"),
	}
}

func (e *testEnv) add(ss ...sysexec.Script) {
	for _, s := range ss {
		e.run.Add(s)
	}
}

// hold is what a first install does before the packages: mask the service,
// stop it and see that it is stopped.
func (e *testEnv) hold() []sysexec.Script { return []sysexec.Script{mask, stop, inactive} }

// scriptInstall scripts a whole successful first install: the service held,
// the packages, the candidate server, the service let go, started and
// checked.
func (e *testEnv) scriptInstall() {
	e.add(e.hold()...)
	e.add(aptUpdate, aptInstall)
	e.add(e.throwaway()...)
	e.add(unmask, reload, enable, restart, livePing, liveCfg, liveACL, liveServer)
}

func noUsers() ([]SiteUser, error) { return nil, nil }

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func lines(calls []sysexec.Call) []string {
	out := make([]string, len(calls))
	for i, c := range calls {
		out[i] = strings.Join(append([]string{c.Path}, c.Args...), " ")
	}
	return out
}

func line(s sysexec.Script) string { return strings.Join(append([]string{s.Path}, s.Args...), " ") }

func missing(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("%s is there (Lstat: %v)", path, err)
	}
}

func wantMode(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != mode {
		t.Errorf("%s: %v, %v; want mode %04o", path, fi, err, mode)
	}
}

const wantDropIn = `# Managed by Zimam. Changes here are overwritten.
[Service]
ExecStart=
ExecStart=/usr/bin/valkey-server /etc/zimam/valkey/valkey.conf --supervised systemd --daemonize no
`

func TestInstallSequence(t *testing.T) {
	e := newEnv(t)
	e.scriptInstall()
	version, err := Install(context.Background(), e.d, noUsers)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if version != "9.0.4" {
		t.Errorf("version = %q, want 9.0.4", version)
	}
	want := []string{
		line(mask), // the package's server must never start on the package's settings
		line(stop),
		line(inactive),
		line(aptUpdate),
		line(aptInstall),
		line(e.throwaway()[0]), // the candidate files, tested on a server of their own
		line(e.throwaway()[1]),
		line(e.throwaway()[2]),
		line(e.throwaway()[3]),
		line(unmask), // only now
		line(reload),
		line(enable),
		line(restart),
		line(livePing),
		line(liveCfg),
		line(liveACL),
		line(liveServer),
	}
	calls := e.run.Calls()
	if got := lines(calls); !slices.Equal(got, want) {
		t.Fatalf("ran\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	for _, c := range calls[3:5] {
		if !slices.Contains(c.Env, "DEBIAN_FRONTEND=noninteractive") {
			t.Errorf("%s ran with env %q", c.Path, c.Env)
		}
	}

	conf := readFile(t, e.p.ValkeyConf())
	for _, w := range []string{"bind 127.0.0.1 -::1\n", "protected-mode yes\n", "port 6379\n", "daemonize no\n", "supervised systemd\n",
		"pidfile /run/valkey/valkey-server.pid\n", "logfile /var/log/valkey/valkey-server.log\n", "dir /var/lib/valkey\n",
		"aclfile /etc/zimam/valkey/users.acl\n", "maxmemory 102mb\n", "maxmemory-policy noeviction\n", "appendonly no\n", "dbfilename dump.rdb\n"} {
		if !strings.Contains(conf, w) {
			t.Errorf("valkey.conf lacks %q:\n%s", w, conf)
		}
	}
	if strings.Contains(conf, "\nsave ") {
		t.Errorf("valkey.conf sets save, which the package leaves at the built-in schedule:\n%s", conf)
	}
	if got := readFile(t, e.p.ValkeyDropIn()); got != wantDropIn {
		t.Errorf("drop-in =\n%s\nwant\n%s", got, wantDropIn)
	}
	wantMode(t, e.p.ValkeyConf(), 0o640)
	wantMode(t, e.p.ValkeyACL(), 0o640)
	wantMode(t, e.p.ValkeyDropIn(), 0o644)
	wantMode(t, e.p.ValkeyAdminPass(), 0o600)
	wantMode(t, e.p.ValkeyDir(), 0o750)

	// What the candidate server was started on is what stays.
	for _, c := range calls[5:7] {
		if c.Path == runuser && !slices.Contains(c.Args, e.p.ValkeyConf()) {
			t.Errorf("the candidate server read %v, not Zimam's config", c.Args)
		}
	}
	st, err := services.Load(e.p)
	if err != nil {
		t.Fatal(err)
	}
	if st.Valkey != (services.Entry{Installed: true, Version: "9.0.4"}) {
		t.Errorf("services.json says %+v", st.Valkey)
	}
	if st.MySQL != (services.Entry{}) {
		t.Errorf("mysql's entry = %+v, want none", st.MySQL)
	}
	// The candidate server is stopped and its directory gone.
	if e.killed.Load() != 1 {
		t.Errorf("the candidate server was stopped %d times, want once", e.killed.Load())
	}
	missing(t, e.p.ValkeyTestDir())
}

func TestDefaultUserOff(t *testing.T) {
	e := newEnv(t)
	e.scriptInstall()
	if _, err := Install(context.Background(), e.d, noUsers); err != nil {
		t.Fatal(err)
	}
	pass := strings.TrimSpace(readFile(t, e.p.ValkeyAdminPass()))
	sum := sha256.Sum256([]byte(pass))
	want := "" +
		"user default off\n" +
		"user zimam-admin on #" + hex.EncodeToString(sum[:]) + " ~* &* +@all\n"
	if got := readFile(t, e.p.ValkeyACL()); got != want {
		t.Errorf("users.acl =\n%s\nwant\n%s", got, want)
	}
}

func TestMaxmemoryFromMemTotal(t *testing.T) {
	for name, tc := range map[string]struct {
		mem  uint64
		want int // MiB
	}{
		"2 GiB gives 204M":                {2 * gib, 204},
		"1 GiB gives 102M":                {1 * gib, 102},
		"a partial MiB is dropped":        {1*gib + 5<<20, 102},
		"64 GiB gives 6553M":              {64 * gib, 6553},
		"256 MiB is raised to the floor":  {256 << 20, 25},
		"a tiny machine gets the floor":   {100 << 20, 16},
		"nothing is raised, never zero":   {0, 16},
		"a 1 GiB machine's real MemTotal": {(1 * gib) - (60 << 20), 96},
		"exactly the floor":               {160 << 20, 16},
		"161 MiB gives the floor":         {161 << 20, 16},
		"the largest memory doesn't wrap": {^uint64(0), int(^uint64(0) / 10 >> 20)},
	} {
		if got := MaxMemoryMiB(tc.mem); got != tc.want {
			t.Errorf("%s: MaxMemoryMiB(%d) = %d, want %d", name, tc.mem, got, tc.want)
		}
	}
}

func TestInstallRendersMaxmemoryFromMemory(t *testing.T) {
	e := newEnv(t)
	if err := os.WriteFile(e.p.ProcMeminfo(), []byte(fmt.Sprintf("MemTotal: %d kB\nMemAvailable: 1 kB\nSwapTotal: 0 kB\nSwapFree: 0 kB\n", 2*gib/1024)), 0o644); err != nil {
		t.Fatal(err)
	}
	e.add(e.hold()...)
	e.add(aptUpdate, aptInstall)
	e.add(e.throwaway()...)
	e.add(unmask, reload, enable, restart, livePing, live(strings.Replace(liveConfig, "106954752", "213909504", 1), "CONFIG", "GET", "bind", "port", "aclfile", "maxmemory", "maxmemory-policy"), liveACL, liveServer)
	if _, err := Install(context.Background(), e.d, noUsers); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, e.p.ValkeyConf()); !strings.Contains(got, "maxmemory 204mb\n") {
		t.Errorf("valkey.conf:\n%s", got)
	}
}

func TestInstallRefusesAnUnreadableMeminfo(t *testing.T) {
	e := newEnv(t)
	if err := os.WriteFile(e.p.ProcMeminfo(), []byte("MemTotal: lots\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Install(context.Background(), e.d, noUsers); err == nil {
		t.Fatal("Install ran with a meminfo it can't read")
	}
	if n := len(e.run.Calls()); n != 0 {
		t.Errorf("ran %d commands before reading the memory", n)
	}
	missing(t, e.p.ValkeyDir())
}

func TestInstallAptFailureWritesNothing(t *testing.T) {
	e := newEnv(t)
	e.add(e.hold()...)
	e.add(aptUpdate, failing(aptInstall, 100, "E: Unable to locate package valkey-server\n"))
	_, err := Install(context.Background(), e.d, noUsers)
	if err == nil || !strings.Contains(err.Error(), "Unable to locate package") {
		t.Fatalf("Install = %v, want apt-get's error", err)
	}
	for _, p := range []string{e.p.ValkeyDir(), e.p.ValkeyDropInDir(), e.p.ServicesState()} {
		missing(t, p)
	}
	// The service was masked and stopped before the packages, and nothing
	// let it go.
	if n := len(e.run.Calls()); n != 5 {
		t.Errorf("ran %d commands, want the hold and apt-get update and install", n)
	}
	if units := unitCalls(lines(e.run.Calls())); !slices.Equal(units, []string{line(mask), line(stop)}) {
		t.Errorf("the service was changed by %v, want it masked and stopped only", units)
	}
}

func TestAdminPasswordGeneratedOnceAndNeverInArgv(t *testing.T) {
	e := newEnv(t)
	e.scriptInstall()
	if _, err := Install(context.Background(), e.d, noUsers); err != nil {
		t.Fatal(err)
	}
	pass := strings.TrimSpace(readFile(t, e.p.ValkeyAdminPass()))
	if len(pass) != 43 || strings.Trim(pass, "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789_-") != "" {
		t.Fatalf("admin.pass holds %q, want 32 random bytes in base64url (43 characters)", pass)
	}

	// A second run, with the files in place, keeps it.
	e.add(aptUpdate, aptInstall, isEnabled, livePing, liveCfg, liveACL, liveServer)
	if _, err := Install(context.Background(), e.d, noUsers); err != nil {
		t.Fatalf("second Install: %v", err)
	}
	if again := strings.TrimSpace(readFile(t, e.p.ValkeyAdminPass())); again != pass {
		t.Errorf("the second run changed the admin password")
	}

	// The password travels only in the environment of valkey-cli.
	var cliRuns int
	for _, c := range e.run.Calls() {
		everything := strings.Join(append(slices.Clone(c.Args), c.Path, c.Dir, string(c.Stdin)), "\x00")
		if strings.Contains(everything, pass) {
			t.Errorf("the password is in the argv, directory or stdin of %s %q", c.Path, c.Args)
		}
		hasEnv := slices.Contains(c.Env, "REDISCLI_AUTH="+pass)
		if c.Path == cliPath {
			cliRuns++
			if !hasEnv {
				t.Errorf("valkey-cli %q ran without REDISCLI_AUTH", c.Args)
			}
			if len(c.Env) != 1 {
				t.Errorf("valkey-cli %q ran with env %q, want only REDISCLI_AUTH", c.Args, c.Env)
			}
		} else if strings.Contains(strings.Join(c.Env, "\x00"), pass) {
			t.Errorf("%s ran with the password in its environment", c.Path)
		}
	}
	if cliRuns == 0 {
		t.Error("valkey-cli never ran")
	}
	for _, f := range []string{readFile(t, e.p.ValkeyConf()), readFile(t, e.p.ValkeyDropIn()), readFile(t, e.p.ValkeyACL()), e.logs.String()} {
		if strings.Contains(f, pass) {
			t.Error("the password is in a rendered file or the job log")
		}
	}
}

func TestAdminPasswordKeptOrReplaced(t *testing.T) {
	good := strings.Repeat("a", 43)
	for name, tc := range map[string]struct {
		setup   func(t *testing.T, e *testEnv)
		keep    bool
		wantErr string
	}{
		"a good password is kept":               {setup: func(t *testing.T, e *testEnv) { writePass(t, e, good+"\n") }, keep: true},
		"one without a newline is kept":         {setup: func(t *testing.T, e *testEnv) { writePass(t, e, good) }, keep: true},
		"an empty file is replaced":             {setup: func(t *testing.T, e *testEnv) { writePass(t, e, "") }},
		"a short one is replaced":               {setup: func(t *testing.T, e *testEnv) { writePass(t, e, "abc\n") }},
		"one with other characters is replaced": {setup: func(t *testing.T, e *testEnv) { writePass(t, e, strings.Repeat("!", 43)) }},
		"a symlink is refused": {setup: func(t *testing.T, e *testEnv) {
			if err := os.MkdirAll(e.p.ValkeyDir(), 0o750); err != nil {
				t.Fatal(err)
			}
			other := filepath.Join(t.TempDir(), "other")
			if err := os.WriteFile(other, []byte(good), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(other, e.p.ValkeyAdminPass()); err != nil {
				t.Fatal(err)
			}
		}, wantErr: "symbolic link"},
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			tc.setup(t, e)
			if tc.wantErr == "" {
				e.scriptInstall()
			} else {
				e.add(e.hold()...)
				e.add(aptUpdate, aptInstall)
			}
			_, err := Install(context.Background(), e.d, noUsers)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("Install = %v, want %q", err, tc.wantErr)
				}
				missing(t, e.p.ValkeyConf())
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			got := strings.TrimSpace(readFile(t, e.p.ValkeyAdminPass()))
			if (got == good) != tc.keep {
				t.Errorf("admin.pass = %q, kept = %v, want kept = %v", got, got == good, tc.keep)
			}
		})
	}
}

func writePass(t *testing.T, e *testEnv, content string) {
	t.Helper()
	if err := os.MkdirAll(e.p.ValkeyDir(), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(e.p.ValkeyAdminPass(), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestInstallConvergedServerIsNotRestarted(t *testing.T) {
	e := newEnv(t)
	e.scriptInstall()
	if _, err := Install(context.Background(), e.d, noUsers); err != nil {
		t.Fatal(err)
	}
	before := len(e.run.Calls())
	conf, acl, dropin := readFile(t, e.p.ValkeyConf()), readFile(t, e.p.ValkeyACL()), readFile(t, e.p.ValkeyDropIn())
	e.add(aptUpdate, aptInstall, isEnabled, livePing, liveCfg, liveACL, liveServer)
	if _, err := Install(context.Background(), e.d, noUsers); err != nil {
		t.Fatalf("second Install: %v", err)
	}
	got := lines(e.run.Calls()[before:])
	want := []string{line(aptUpdate), line(aptInstall), line(isEnabled), line(livePing), line(liveCfg), line(liveACL), line(liveServer)}
	if !slices.Equal(got, want) {
		t.Errorf("the second run ran\n%s\nwant only the packages and the live checks:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if readFile(t, e.p.ValkeyConf()) != conf || readFile(t, e.p.ValkeyACL()) != acl || readFile(t, e.p.ValkeyDropIn()) != dropin {
		t.Error("the second run changed a file")
	}
	for _, p := range []string{e.p.ValkeyConf() + ".zimam-prev", e.p.ValkeyACL() + ".zimam-prev", e.p.ValkeyDropIn() + ".zimam-prev"} {
		missing(t, p)
	}
	if e.killed.Load() != 1 {
		t.Errorf("a candidate server ran in the second run (stopped %d times in all)", e.killed.Load())
	}
}

// Files in place are no proof of what the server runs: it may be stopped,
// or running older settings.
func TestInstallRestartsWhenAnUnchangedFileIsNotLive(t *testing.T) {
	masked := sysexec.Script{Path: isEnabled.Path, Args: isEnabled.Args, Result: sysexec.Result{ExitCode: 1, Stdout: []byte("masked\n")}}
	disabled := sysexec.Script{Path: isEnabled.Path, Args: isEnabled.Args, Result: sysexec.Result{ExitCode: 1, Stdout: []byte("disabled\n")}}
	for name, tc := range map[string]struct {
		cfg, acl sysexec.Script
		ping     sysexec.Script
		enabled  sysexec.Script
	}{
		"the server is down":          {liveCfg, liveACL, failing(livePing, 1, "Could not connect to Valkey at 127.0.0.1:6379: Connection refused\n"), isEnabled},
		"it listens elsewhere":        {live(strings.Replace(liveConfig, "127.0.0.1 -::1", "* -::*", 1), "CONFIG", "GET", "bind", "port", "aclfile", "maxmemory", "maxmemory-policy"), liveACL, livePing, isEnabled},
		"it has another ACL file":     {live(strings.Replace(liveConfig, "/etc/zimam/valkey/users.acl", "/etc/valkey/users.acl", 1), "CONFIG", "GET", "bind", "port", "aclfile", "maxmemory", "maxmemory-policy"), liveACL, livePing, isEnabled},
		"its default user is on":      {liveCfg, live("user default on nopass ~* &* +@all\n", "ACL", "LIST"), livePing, isEnabled},
		"it has another memory limit": {live(strings.Replace(liveConfig, "106954752", "0", 1), "CONFIG", "GET", "bind", "port", "aclfile", "maxmemory", "maxmemory-policy"), liveACL, livePing, isEnabled},
		// A server that answers is no proof the service is usable: a run that was
		// killed after stopping and masking it, or an administrator, may have
		// left it masked or disabled, and it comes back at the next restart.
		"the service is masked":   {liveCfg, liveACL, livePing, masked},
		"the service is disabled": {liveCfg, liveACL, livePing, disabled},
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			e.scriptInstall()
			if _, err := Install(context.Background(), e.d, noUsers); err != nil {
				t.Fatal(err)
			}
			before := len(e.run.Calls())
			// The second run's checks fail, then the restart's pass.
			e.add(aptUpdate, aptInstall, tc.enabled, tc.ping, tc.cfg, tc.acl)
			e.add(e.throwaway()...)
			e.add(unmask, enable, restart, livePing, liveCfg, liveACL, liveServer)
			if _, err := Install(context.Background(), e.d, noUsers); err != nil {
				t.Fatalf("second Install: %v", err)
			}
			got := lines(e.run.Calls()[before:])
			if !slices.Contains(got, line(restart)) {
				t.Errorf("the second run never restarted:\n%s", strings.Join(got, "\n"))
			}
			// Whatever state the service was left in, the run ends with it
			// unmasked, enabled and started.
			if units := unitCalls(got); !slices.Equal(units, []string{line(unmask), line(enable), line(restart)}) {
				t.Errorf("the service was changed by %v, want unmask, enable and restart", units)
			}
			if slices.Contains(got, line(reload)) {
				t.Errorf("the drop-in didn't change, but the second run reloaded systemd:\n%s", strings.Join(got, "\n"))
			}
		})
	}
}

const (
	prevConf   = "port 6379\n"
	prevACL    = "user default off\n"
	prevDropIn = "[Service]\nExecStart=\nExecStart=/usr/bin/valkey-server /etc/zimam/valkey/valkey.conf --daemonize no\n"
)

// withPrevious puts files in place that a failed change must bring back.
func withPrevious(t *testing.T, e *testEnv) {
	t.Helper()
	for path, content := range map[string]string{e.p.ValkeyConf(): prevConf, e.p.ValkeyACL(): prevACL, e.p.ValkeyDropIn(): prevDropIn} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o640); err != nil {
			t.Fatal(err)
		}
	}
}

// unitUp is what brings the real server up on the candidate files: for a
// first install the service is unmasked and enabled first.
func unitUp(first bool) []sysexec.Script {
	_ = first
	return []sysexec.Script{unmask, reload, enable, restart}
}

// undoScripts is what puts the service back after the server read the files.
func undoScripts(first bool) []sysexec.Script {
	if first {
		return []sysexec.Script{reload, stop, mask}
	}
	return []sysexec.Script{reload, resetFailed, restart}
}

// unitCalls are the calls that change the service.
func unitCalls(calls []string) []string {
	var out []string
	for _, c := range calls {
		if slices.Contains([]string{line(mask), line(unmask), line(stop), line(enable), line(restart)}, c) {
			out = append(out, c)
		}
	}
	return out
}

// The same failures, on a first install (nothing of Zimam's in place, the
// service masked) and on a re-run (Zimam's files in place and running).
func TestBadConfigRestoresPrevious(t *testing.T) {
	badConfig := "*** FATAL CONFIG FILE ERROR (Version 9.0.4) ***\n>>> 'maxmemory lots'\n"
	cfgWith := func(old, new string) sysexec.Script {
		return live(strings.Replace(liveConfig, old, new, 1), "CONFIG", "GET", "bind", "port", "aclfile", "maxmemory", "maxmemory-policy")
	}
	restartFails := failing(restart, 1, "Job for valkey-server.service failed because the control process exited with error code.\n")
	aclDefaultOn := live("user default on nopass ~* &* +@all\nuser zimam-admin on #abc ~* &* +@all\n", "ACL", "LIST")
	type tc struct {
		steps func(e *testEnv, first bool) []sysexec.Script // after the packages, in the order they run
		want  string                                        // in the error
		small bool                                          // run with a short wait for the candidate server
	}
	for name, tc := range map[string]tc{
		"the candidate server refuses the file": {
			// Nothing else runs: the real server never read the file.
			steps: func(e *testEnv, _ bool) []sysexec.Script {
				return []sysexec.Script{failing(e.throwaway()[0], 1, badConfig)}
			},
			want: "FATAL CONFIG FILE ERROR",
		},
		"the candidate server never answers": {
			steps: func(e *testEnv, _ bool) []sysexec.Script {
				return []sysexec.Script{e.throwaway()[0], failing(e.throwaway()[1], 1, "AUTH failed: WRONGPASS invalid username-password pair or user is disabled.\n")}
			},
			want:  "WRONGPASS",
			small: true,
		},
		"systemd can't reload": {
			steps: func(e *testEnv, first bool) []sysexec.Script {
				s := e.throwaway()
				s = append(s, unmask)
				// The server never read the files, so a re-run restarts nothing.
				s = append(s, failing(reload, 1, "Failed to reload daemon\n"), reload)
				if first {
					s = append(s, stop, mask)
				}
				return s
			},
			want: "Failed to reload",
		},
		"the restart fails": {
			steps: func(e *testEnv, first bool) []sysexec.Script {
				up := unitUp(first)
				up[len(up)-1] = restartFails
				return append(append(e.throwaway(), up...), undoScripts(first)...)
			},
			want: "Job for valkey-server.service failed",
		},
		"the admin can't log in after the restart": {
			steps: func(e *testEnv, first bool) []sysexec.Script {
				s := append(e.throwaway(), unitUp(first)...)
				return append(append(s, failing(livePing, 1, "AUTH failed: WRONGPASS invalid username-password pair\n")), undoScripts(first)...)
			},
			want: "WRONGPASS",
		},
		"it listens on every address": {
			steps: func(e *testEnv, first bool) []sysexec.Script {
				s := append(e.throwaway(), unitUp(first)...)
				return append(append(s, livePing, cfgWith("127.0.0.1 -::1", "* -::*"), liveACL, liveServer), undoScripts(first)...)
			},
			want: "bind is",
		},
		"it has no memory limit": {
			steps: func(e *testEnv, first bool) []sysexec.Script {
				s := append(e.throwaway(), unitUp(first)...)
				return append(append(s, livePing, cfgWith("106954752", "0"), liveACL, liveServer), undoScripts(first)...)
			},
			want: "maxmemory is",
		},
		"its default user is still on": {
			steps: func(e *testEnv, first bool) []sysexec.Script {
				s := append(e.throwaway(), unitUp(first)...)
				return append(append(s, livePing, liveCfg, aclDefaultOn, liveServer), undoScripts(first)...)
			},
			want: "default user",
		},
		"it has users the file doesn't": {
			steps: func(e *testEnv, first bool) []sysexec.Script {
				s := append(e.throwaway(), unitUp(first)...)
				extra := live("user default off\nuser zimam-admin on #abc ~* &* +@all\nuser s_x on #abc ~* +@all\n", "ACL", "LIST")
				return append(append(s, livePing, liveCfg, extra, liveServer), undoScripts(first)...)
			},
			want: "its users are",
		},
	} {
		for _, first := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s (first install: %v)", name, first), func(t *testing.T) {
				if tc.small {
					old := startTimeout
					startTimeout = 300 * time.Millisecond
					t.Cleanup(func() { startTimeout = old })
				}
				e := newEnv(t)
				pre := 0
				if first {
					e.add(e.hold()...)
					pre = 3
				} else {
					withPrevious(t, e)
				}
				e.add(aptUpdate, aptInstall)
				steps := tc.steps(e, first)
				e.add(steps...)
				_, err := Install(context.Background(), e.d, noUsers)
				if err == nil || !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("Install = %v, want an error holding %q", err, tc.want)
				}
				if strings.Contains(err.Error(), "also") {
					t.Errorf("the restore failed too: %v", err)
				}
				for path, content := range map[string]string{e.p.ValkeyConf(): prevConf, e.p.ValkeyACL(): prevACL, e.p.ValkeyDropIn(): prevDropIn} {
					if first {
						missing(t, path)
						continue
					}
					if got := readFile(t, path); got != content {
						t.Errorf("%s =\n%s\nwant the previous file back:\n%s", path, got, content)
					}
					missing(t, path+".zimam-prev")
				}
				got := lines(e.run.Calls())[pre+2:]
				if tc.small {
					got = slices.Compact(got) // the candidate's PING is retried until the wait ends
				}
				var want []string
				for _, s := range steps {
					want = append(want, line(s))
				}
				if !slices.Equal(got, want) {
					t.Errorf("after the packages ran\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
				}
				if first {
					// The package's server never runs on its own settings: after a failed
					// first install the service is stopped and masked, and nothing
					// started it again after the failure.
					units := unitCalls(lines(e.run.Calls()))
					if slices.Contains(units, line(unmask)) {
						if last := units[len(units)-1]; last != line(mask) {
							t.Errorf("the last change to the service was %s, want it masked again", last)
						}
					} else if slices.Contains(units, line(restart)) || slices.Contains(units, line(enable)) {
						t.Errorf("the service was started without being unmasked: %v", units)
					}
					if n := slices.Index(units, line(restart)); n >= 0 && slices.Contains(units[n+1:], line(restart)) {
						t.Errorf("the package's settings were started again after the failure: %v", units)
					}
				}
				missing(t, e.p.ServicesState())
				missing(t, e.p.ValkeyTestDir())
				if e.killed.Load() > 1 {
					t.Errorf("the candidate server was stopped %d times", e.killed.Load())
				}
				// A new admin password is part of the files that were refused.
				missing(t, e.p.ValkeyAdminPass())
			})
		}
	}
}

// A server that never starts is stopped and its directory removed, when
// the job is cancelled while the candidate runs, too.
func TestCancelledWhileTheCandidateRuns(t *testing.T) {
	e := newEnv(t)
	withPrevious(t, e)
	e.srv.noSocket = true
	e.d.Run = e.srv
	e.add(aptUpdate, aptInstall)
	e.add(e.throwaway()[0])
	ctx, cancel := context.WithCancel(context.Background())
	e.srv.onCall = func(c sysexec.Cmd, _ context.Context) error {
		if c.Path == runuser {
			go func() { time.Sleep(100 * time.Millisecond); cancel() }()
		}
		return nil
	}
	e.d.Run = e.srv
	_, err := Install(ctx, e.d, noUsers)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Install = %v, want the cancel", err)
	}
	if e.killed.Load() != 1 {
		t.Errorf("the candidate server was stopped %d times, want once", e.killed.Load())
	}
	missing(t, e.p.ValkeyTestDir())
	if got := readFile(t, e.p.ValkeyConf()); got != prevConf {
		t.Errorf("valkey.conf = %q, want the previous file", got)
	}
	// The server never read the candidate, so nothing was restarted.
	for _, l := range lines(e.run.Calls()) {
		if l == line(restart) || l == line(reload) {
			t.Errorf("ran %s although the real server never read the candidate", l)
		}
	}
}

// A cancel that ends the restart must not stop the restore: the server
// would be left running settings that are not on disk.
func TestCancelledDuringTheRestartStillRestores(t *testing.T) {
	e := newEnv(t)
	withPrevious(t, e)
	e.add(aptUpdate, aptInstall)
	e.add(e.throwaway()...)
	e.add(unmask, reload, enable, restart, reload, resetFailed, restart)
	ctx, cancel := context.WithCancel(context.Background())
	var restarts, restoreCtxErr atomic.Int32
	e.srv.onCall = func(c sysexec.Cmd, cctx context.Context) error {
		if c.Path == systemctl && slices.Equal(c.Args, restart.Args) {
			if restarts.Add(1) == 1 {
				cancel() // the job is cancelled as the candidate's restart starts
				return context.Canceled
			}
			if cctx.Err() != nil {
				restoreCtxErr.Add(1)
				return cctx.Err()
			}
		}
		return nil
	}
	e.d.Run = e.srv
	_, err := Install(ctx, e.d, noUsers)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Install = %v, want the cancel", err)
	}
	if restoreCtxErr.Load() != 0 {
		t.Error("the restore's restart ran with a cancelled context")
	}
	if restarts.Load() != 2 {
		t.Errorf("restarted %d times, want the candidate's and the restore's", restarts.Load())
	}
	if got := readFile(t, e.p.ValkeyConf()); got != prevConf {
		t.Errorf("valkey.conf = %q, want the previous file", got)
	}
	missing(t, e.p.ServicesState())
}

// A restart that never returns is a failure, and the job's limit has room
// for the restore after it.
func TestHangingRestartIsAFailureAndRestoresPrevious(t *testing.T) {
	old := restartTimeout
	restartTimeout = 150 * time.Millisecond
	t.Cleanup(func() { restartTimeout = old })

	e := newEnv(t)
	withPrevious(t, e)
	e.add(aptUpdate, aptInstall)
	e.add(e.throwaway()...)
	e.add(unmask, reload, enable, restart, reload, resetFailed, restart)
	var restarts atomic.Int32
	e.srv.onCall = func(c sysexec.Cmd, ctx context.Context) error {
		if c.Path == systemctl && slices.Equal(c.Args, restart.Args) && restarts.Add(1) == 1 {
			<-ctx.Done()
			return ctx.Err()
		}
		return nil
	}
	e.d.Run = e.srv
	_, err := Install(context.Background(), e.d, noUsers)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Install = %v, want the restart's timeout", err)
	}
	if !strings.Contains(err.Error(), "took longer than") {
		t.Errorf("the error doesn't say the restart took too long: %v", err)
	}
	if got := readFile(t, e.p.ValkeyConf()); got != prevConf {
		t.Errorf("valkey.conf = %q, want the previous file back", got)
	}
	if restarts.Load() != 2 {
		t.Errorf("restarted %d times, want the hung one and the restore's", restarts.Load())
	}
}

// A restore that fails says the server may be running settings that are not
// on disk.
func TestFailedRestoreSaysTheServerMayRunOtherSettings(t *testing.T) {
	e := newEnv(t)
	withPrevious(t, e)
	e.add(aptUpdate, aptInstall)
	e.add(e.throwaway()...)
	e.add(unmask, reload, enable, failing(restart, 1, "Job failed\n"), reload, failing(restart, 1, "Job failed again\n"))
	_, err := Install(context.Background(), e.d, noUsers)
	if err == nil || !strings.Contains(err.Error(), "may be running settings that are not in") {
		t.Fatalf("Install = %v, want the warning about the server's settings", err)
	}
}

// aclWith is ACL LIST for a server with the default user off, the admin and
// these site users.
func aclWith(names ...string) sysexec.Script {
	out := "user default off sanitize-payload resetchannels -@all\nuser zimam-admin on sanitize-payload #abc ~* &* +@all\n"
	for _, n := range names {
		out += "user " + n + " on sanitize-payload #abc resetkeys ~" + n + ":* +@all\n"
	}
	return live(out, "ACL", "LIST")
}

func TestSiteUsersAreKeptAndSorted(t *testing.T) {
	e := newEnv(t)
	e.add(e.hold()...)
	e.add(aptUpdate, aptInstall)
	e.add(e.throwaway("s_blog", "s_shop")...)
	e.add(unmask, reload, enable, restart, livePing, liveCfg, aclWith("s_blog", "s_shop"), liveServer)
	a, b := sha256.Sum256([]byte("a password")), sha256.Sum256([]byte("b password"))
	users := func() ([]SiteUser, error) {
		return []SiteUser{{Name: "shop", Hash: b}, {Name: "blog", Hash: a}}, nil
	}
	if _, err := Install(context.Background(), e.d, users); err != nil {
		t.Fatal(err)
	}
	got := readFile(t, e.p.ValkeyACL())
	wantBlog := "user s_blog on #" + hex.EncodeToString(a[:]) + " resetkeys ~s_blog:* resetchannels &s_blog:* +@all -@admin -@dangerous -function -script|kill -script|flush -script|debug\n"
	wantShop := "user s_shop on #" + hex.EncodeToString(b[:]) + " resetkeys ~s_shop:* resetchannels &s_shop:* +@all -@admin -@dangerous -function -script|kill -script|flush -script|debug\n"
	if !strings.HasSuffix(got, wantBlog+wantShop) {
		t.Errorf("users.acl =\n%s\nwant it to end with the site users, sorted:\n%s%s", got, wantBlog, wantShop)
	}
	if strings.Count(got, "user default off\n") != 1 || !strings.Contains(got, " ~* &* +@all\n") {
		t.Errorf("users.acl lost the default or admin line:\n%s", got)
	}
}

// A site's user that exists when valkey.install runs again must not be lost
// from the file: the file is rendered from the sites' state every time.
func TestRerunKeepsSiteUsers(t *testing.T) {
	e := newEnv(t)
	e.scriptInstall()
	if _, err := Install(context.Background(), e.d, noUsers); err != nil {
		t.Fatal(err)
	}
	h := sha256.Sum256([]byte("x"))
	users := func() ([]SiteUser, error) { return []SiteUser{{Name: "blog", Hash: h}}, nil }
	// A site user appeared since: the ACL file changes, so the server restarts.
	e.add(aptUpdate, aptInstall)
	e.add(e.throwaway("s_blog")...)
	e.add(unmask, enable, restart, livePing, liveCfg, aclWith("s_blog"), liveServer)
	if _, err := Install(context.Background(), e.d, users); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, e.p.ValkeyACL()); !strings.Contains(got, "user s_blog on #"+hex.EncodeToString(h[:])) {
		t.Errorf("users.acl lost the site user:\n%s", got)
	}
	// And with it in place, the next run changes nothing.
	before := len(e.run.Calls())
	e.add(aptUpdate, aptInstall, isEnabled, livePing, liveCfg, aclWith("s_blog"), liveServer)
	if _, err := Install(context.Background(), e.d, users); err != nil {
		t.Fatal(err)
	}
	if n := len(e.run.Calls()) - before; n != 7 {
		t.Errorf("the run with nothing new made %d calls, want 7", n)
	}
}

func TestSiteUsersFailingChangesNothing(t *testing.T) {
	e := newEnv(t)
	e.add(e.hold()...)
	e.add(aptUpdate, aptInstall)
	_, err := Install(context.Background(), e.d, func() ([]SiteUser, error) { return nil, errors.New("shop.json is corrupt") })
	if err == nil || !strings.Contains(err.Error(), "shop.json is corrupt") {
		t.Fatalf("Install = %v", err)
	}
	missing(t, e.p.ValkeyConf())
	missing(t, e.p.ValkeyACL())
	missing(t, e.p.ServicesState())
	if n := len(e.run.Calls()); n != 5 {
		t.Errorf("ran %d commands, want only the hold and the packages", n)
	}
}

// Two writers of the ACL file, this install and a site's cache user, take
// one lock, so they never interleave.
func TestInstallWaitsForTheACLLock(t *testing.T) {
	e := newEnv(t)
	e.add(e.hold()...)
	e.add(aptUpdate, aptInstall)
	unlock, err := sysexec.Lock(context.Background(), e.p.ValkeyACLLock())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	_, err = Install(ctx, e.d, noUsers)
	unlock()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Install = %v, want to be still waiting for the lock", err)
	}
	missing(t, e.p.ValkeyConf())
	missing(t, e.p.ValkeyAdminPass())
}

// A stale candidate directory, from a job that was killed, is cleared
// before the next test of the files.
func TestStaleCandidateDirectoryIsCleared(t *testing.T) {
	e := newEnv(t)
	stale := filepath.Join(e.p.ValkeyTestDir(), "old")
	if err := os.MkdirAll(stale, 0o700); err != nil {
		t.Fatal(err)
	}
	e.scriptInstall()
	if _, err := Install(context.Background(), e.d, noUsers); err != nil {
		t.Fatal(err)
	}
	missing(t, stale)
	missing(t, e.p.ValkeyTestDir())
}

func TestTestFailureIsAConfigTestError(t *testing.T) {
	e := newEnv(t)
	e.add(e.hold()...)
	e.add(aptUpdate, aptInstall, failing(e.throwaway()[0], 1, "*** FATAL CONFIG FILE ERROR ***\n"))
	_, err := Install(context.Background(), e.d, noUsers)
	var tf *configtx.TestFailed
	if !errors.As(err, &tf) {
		t.Fatalf("Install = %v, want a *configtx.TestFailed", err)
	}
}

// A parent directory that doesn't exist yet is made readable by everyone: the
// valkey user passes through it to reach the config.
func TestParentDirectoriesAreWorldReadable(t *testing.T) {
	e := newEnv(t)
	e.scriptInstall()
	if _, err := Install(context.Background(), e.d, noUsers); err != nil {
		t.Fatal(err)
	}
	wantMode(t, filepath.Dir(e.p.ValkeyDir()), 0o755)
	wantMode(t, filepath.Dir(e.p.ValkeyDropInDir()), 0o755)
}

// Clearing systemd's start counter is a courtesy to the restore: when it
// fails, the server is still restarted.
func TestRestoreRestartsEvenIfResetFailedFails(t *testing.T) {
	e := newEnv(t)
	withPrevious(t, e)
	e.add(aptUpdate, aptInstall)
	e.add(e.throwaway()...)
	e.add(unmask, reload, enable, failing(restart, 1, "Job failed\n"), reload, failing(resetFailed, 1, "Failed\n"), restart)
	_, err := Install(context.Background(), e.d, noUsers)
	if err == nil || strings.Contains(err.Error(), "may be running") {
		t.Fatalf("Install = %v, want only the first failure", err)
	}
	got := lines(e.run.Calls())
	if got[len(got)-1] != line(restart) || got[len(got)-2] != line(resetFailed) {
		t.Errorf("the last calls were\n%s\nwant reset-failed and then a restart", strings.Join(got[len(got)-3:], "\n"))
	}
}

// Data left in the package's data directory from before Zimam managed Valkey
// could hold keys planted by anyone, so Zimam's server must not load it.
func TestExistingDataIsRefused(t *testing.T) {
	for name, plant := range map[string]func(t *testing.T, dir string){
		"a dump": func(t *testing.T, dir string) {
			if err := os.WriteFile(filepath.Join(dir, "dump.rdb"), []byte("REDIS"), 0o600); err != nil {
				t.Fatal(err)
			}
		},
		"an append-only directory with files": func(t *testing.T, dir string) {
			if err := os.MkdirAll(filepath.Join(dir, "appendonlydir"), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "appendonlydir", "appendonly.aof.1.base.rdb"), []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			if err := os.MkdirAll(e.p.ValkeyDataDir(), 0o750); err != nil {
				t.Fatal(err)
			}
			plant(t, e.p.ValkeyDataDir())
			e.add(e.hold()...)
			e.add(aptUpdate, aptInstall)
			_, err := Install(context.Background(), e.d, noUsers)
			var ae *contract.AgentError
			if !errors.As(err, &ae) || ae.Code != contract.ErrConflict {
				t.Fatalf("Install = %v, want a conflict", err)
			}
			if !strings.Contains(err.Error(), "dump.rdb") && !strings.Contains(err.Error(), "appendonlydir") {
				t.Errorf("the error doesn't name the data: %v", err)
			}
			// The service stays stopped and masked, nothing is written, and the
			// data is where it was.
			if units := unitCalls(lines(e.run.Calls())); !slices.Equal(units, []string{line(mask), line(stop)}) {
				t.Errorf("the service was changed by %v, want it masked and stopped only", units)
			}
			for _, p := range []string{e.p.ValkeyDir(), e.p.ValkeyConf(), e.p.ServicesState()} {
				missing(t, p)
			}
			if entries, _ := os.ReadDir(e.p.ValkeyDataDir()); len(entries) != 1 {
				t.Errorf("the data directory holds %d entries, want what was there", len(entries))
			}
			if !strings.Contains(e.logs.String(), "stopped and masked") {
				t.Errorf("the job log doesn't say the service is masked:\n%s", e.logs.String())
			}
		})
	}
}

func TestAnEmptyAppendOnlyDirectoryIsNoData(t *testing.T) {
	e := newEnv(t)
	if err := os.MkdirAll(filepath.Join(e.p.ValkeyDataDir(), "appendonlydir"), 0o700); err != nil {
		t.Fatal(err)
	}
	e.scriptInstall()
	if _, err := Install(context.Background(), e.d, noUsers); err != nil {
		t.Fatalf("Install: %v", err)
	}
}

// A server an administrator left running on the package's settings is
// stopped first; one that won't stop stops the install before anything else.
func TestARunningServerThatWontStopStopsTheInstall(t *testing.T) {
	e := newEnv(t)
	running := sysexec.Script{Path: systemctl, Args: inactive.Args} // exit 0: active
	e.add(mask, stop, running)
	_, err := Install(context.Background(), e.d, noUsers)
	if err == nil || !strings.Contains(err.Error(), "still running") {
		t.Fatalf("Install = %v, want it to refuse a server it can't stop", err)
	}
	if n := len(e.run.Calls()); n != 3 {
		t.Errorf("ran %d commands, want only the hold", n)
	}
}

// The dump a first start leaves when the service is stopped again is Zimam's
// own, and is removed so that the next install doesn't refuse it.
func TestFailedFirstInstallRemovesTheDumpItCaused(t *testing.T) {
	e := newEnv(t)
	e.add(e.hold()...)
	e.add(aptUpdate, aptInstall)
	e.add(e.throwaway()...)
	e.add(unmask, reload, enable, failing(restart, 1, "Job failed\n"), reload, stop, mask)
	dump := filepath.Join(e.p.ValkeyDataDir(), "dump.rdb")
	var stops int
	e.srv.onCall = func(c sysexec.Cmd, _ context.Context) error {
		if c.Path == systemctl && slices.Equal(c.Args, stop.Args) {
			stops++
			if stops == 2 {
				// The server saves a dump as it stops: this is the stop of the
				// undo, after the hold's.
				_ = os.MkdirAll(e.p.ValkeyDataDir(), 0o750)
				_ = os.WriteFile(dump, []byte("REDIS"), 0o600)
			}
		}
		return nil
	}
	e.d.Run = e.srv
	if _, err := Install(context.Background(), e.d, noUsers); err == nil {
		t.Fatal("Install succeeded with a restart that fails")
	}
	if stops != 2 {
		t.Fatalf("the service was stopped %d times, want the hold's and the undo's", stops)
	}
	missing(t, dump)
}

// The undo of a failure may not be cut off by the job's time limit, so a
// deadline that leaves less than it can need ends the install before any file
// is staged.
func TestShortDeadlineStopsTheInstallBeforeStagingFiles(t *testing.T) {
	e := newEnv(t)
	e.add(e.hold()...)
	e.add(aptUpdate, aptInstall)
	ctx, cancel := context.WithTimeout(context.Background(), needed()-time.Minute)
	defer cancel()
	_, err := Install(ctx, e.d, noUsers)
	if err == nil || !strings.Contains(err.Error(), "of this job's time is left") {
		t.Fatalf("Install = %v, want it to refuse for lack of time", err)
	}
	for _, p := range []string{e.p.ValkeyConf(), e.p.ValkeyACL(), e.p.ValkeyAdminPass(), e.p.ServicesState()} {
		missing(t, p)
	}
	if units := unitCalls(lines(e.run.Calls())); !slices.Equal(units, []string{line(mask), line(stop)}) {
		t.Errorf("the service was changed by %v, want it masked and stopped only", units)
	}
	// With the time there, the same install goes through.
	e2 := newEnv(t)
	e2.scriptInstall()
	ctx2, cancel2 := context.WithTimeout(context.Background(), needed()+time.Minute)
	defer cancel2()
	if _, err := Install(ctx2, e2.d, noUsers); err != nil {
		t.Fatalf("Install with enough time: %v", err)
	}
}

func TestNeededFitsTheJobLimit(t *testing.T) {
	// Outside needed() are the hold before the packages (10 + 60 + 10 s) and
	// apt-get's lock wait (300 s); the job's 900 s must hold them all, with
	// the lock on Valkey's users.
	total := lockWait + 80*time.Second + needed() + 300*time.Second
	if total > 900*time.Second {
		t.Errorf("the steps add up to %s, more than the job's 900 s", total)
	}
}

// A new admin password is written with the other files, so a failed test
// puts back the one that matches the ACL file it restores.
func TestFailedTestKeepsTheOldAdminPassword(t *testing.T) {
	e := newEnv(t)
	withPrevious(t, e)
	writePass(t, e, "not a password\n")
	e.add(aptUpdate, aptInstall)
	e.add(failing(e.throwaway()[0], 1, "*** FATAL CONFIG FILE ERROR ***\n"))
	if _, err := Install(context.Background(), e.d, noUsers); err == nil {
		t.Fatal("Install succeeded")
	}
	if got := readFile(t, e.p.ValkeyAdminPass()); got != "not a password\n" {
		t.Errorf("admin.pass = %q, want the file that was there", got)
	}
	missing(t, e.p.ValkeyAdminPass()+".zimam-prev")
	if got := readFile(t, e.p.ValkeyACL()); got != prevACL {
		t.Errorf("users.acl = %q, want the previous one", got)
	}
}

// A candidate server that a killed job left running is ended before its
// directory is removed, if /proc shows it is a valkey-server of user valkey.
func TestStaleCandidateServerIsKilled(t *testing.T) {
	for name, tc := range map[string]struct {
		exe     string // the process name
		uid     int
		cmdline string // what the process was started with, DIR for the candidate's directory
		kill    bool
	}{
		"the candidate": {"valkey-server", os.Getuid(), "valkey-server\x00unixsocket:DIR/s.sock\x00", true},
		// A recycled pid may be the real server's: same name, same user.
		"the real server": {"valkey-server", os.Getuid(), "/usr/bin/valkey-server\x00127.0.0.1:6379\x00", false},
		"another program": {"sleep", os.Getuid(), "sleep\x00DIR\x00", false},
		"another user's":  {"valkey-server", os.Getuid() + 1, "valkey-server\x00unixsocket:DIR/s.sock\x00", false},
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			var killed []int
			old := killProcess
			killProcess = func(pid int, _ syscall.Signal) error { killed = append(killed, pid); return nil }
			t.Cleanup(func() { killProcess = old })
			dir := e.p.ValkeyTestDir()
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "p"), []byte("4242\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			proc := e.p.ProcPID("4242")
			if err := os.MkdirAll(proc, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(proc, "comm"), []byte(tc.exe+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			cmdline := strings.ReplaceAll(tc.cmdline, "DIR", dir)
			if err := os.WriteFile(filepath.Join(proc, "cmdline"), []byte(cmdline), 0o644); err != nil {
				t.Fatal(err)
			}
			status := fmt.Sprintf("Name:\tvalkey-server\nUid:\t%d\t%d\t%d\t%d\n", tc.uid, tc.uid, tc.uid, tc.uid)
			if err := os.WriteFile(filepath.Join(proc, "status"), []byte(status), 0o644); err != nil {
				t.Fatal(err)
			}
			e.scriptInstall()
			if _, err := Install(context.Background(), e.d, noUsers); err != nil {
				t.Fatal(err)
			}
			if got := slices.Equal(killed, []int{4242}); got != tc.kill {
				t.Errorf("killed %v, want a kill of 4242: %v", killed, tc.kill)
			}
			missing(t, dir)
		})
	}
}

// A pidfile with nonsense, or the pid of init, kills nothing.
func TestStalePidfileNeverKillsInit(t *testing.T) {
	e := newEnv(t)
	old := killProcess
	killProcess = func(pid int, _ syscall.Signal) error { t.Errorf("killed %d", pid); return nil }
	t.Cleanup(func() { killProcess = old })
	for _, content := range []string{"1\n", "0\n", "-5\n", "abc\n", ""} {
		dir := e.p.ValkeyTestDir()
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "p"), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		killStale(e.d, dir, os.Getuid())
	}
}

// A server whose users in memory differ from the file, such as one an
// administrator changed with ACL commands, is not converged.
func TestSkipPathComparesTheLiveUsers(t *testing.T) {
	e := newEnv(t)
	h := sha256.Sum256([]byte("x"))
	users := func() ([]SiteUser, error) { return []SiteUser{{Name: "blog", Hash: h}}, nil }
	e.add(e.hold()...)
	e.add(aptUpdate, aptInstall)
	e.add(e.throwaway("s_blog")...)
	e.add(unmask, reload, enable, restart, livePing, liveCfg, aclWith("s_blog"), liveServer)
	if _, err := Install(context.Background(), e.d, users); err != nil {
		t.Fatal(err)
	}
	before := len(e.run.Calls())
	// The file is as before, but the server has lost the site's user.
	e.add(aptUpdate, aptInstall, isEnabled, livePing, liveCfg, aclWith(), liveServer)
	e.add(e.throwaway("s_blog")...)
	e.add(unmask, enable, restart, livePing, liveCfg, aclWith("s_blog"), liveServer)
	if _, err := Install(context.Background(), e.d, users); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(lines(e.run.Calls()[before:]), line(restart)) {
		t.Error("a server missing a user of the file was not restarted")
	}
}

func TestFailureNotes(t *testing.T) {
	if got := failureNote(true, false); !strings.Contains(got, "stopped and masked") {
		t.Errorf("first install: %q", got)
	}
	got := failureNote(false, true)
	if strings.Contains(got, "not recorded") || strings.Contains(got, "masked") {
		t.Errorf("a re-run on a recorded server says %q", got)
	}
}

// recordedWithoutDropIn is a server Zimam installed Valkey on, with a file of
// its settings gone and data in the data directory, which is the sites' own.
func recordedWithoutDropIn(t *testing.T, e *testEnv) string {
	t.Helper()
	withPrevious(t, e)
	if err := os.Remove(e.p.ValkeyDropIn()); err != nil {
		t.Fatal(err)
	}
	if err := services.Update(e.p, func(s *services.State) { s.Valkey = services.Entry{Installed: true, Version: "9.0.4"} }); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(e.p.ValkeyDataDir(), 0o750); err != nil {
		t.Fatal(err)
	}
	dump := filepath.Join(e.p.ValkeyDataDir(), "dump.rdb")
	if err := os.WriteFile(dump, []byte("REDIS0012 the sites' keys"), 0o600); err != nil {
		t.Fatal(err)
	}
	return dump
}

// A recorded server with a file missing is held, since there is no complete
// configuration of Zimam's to run, but its data is the sites' own: it is not
// refused, not deleted, and the server comes back on the new files.
func TestRecordedServerWithAFileMissingIsHeldThenHealed(t *testing.T) {
	e := newEnv(t)
	dump := recordedWithoutDropIn(t, e)
	e.add(e.hold()...)
	e.add(aptUpdate, aptInstall)
	e.add(e.throwaway()...)
	e.add(unmask, reload, enable, restart, livePing, liveCfg, liveACL, liveServer)
	if _, err := Install(context.Background(), e.d, noUsers); err != nil {
		t.Fatalf("Install: %v", err)
	}
	want := []string{line(mask), line(stop), line(unmask), line(enable), line(restart)}
	if units := unitCalls(lines(e.run.Calls())); !slices.Equal(units, want) {
		t.Errorf("the service was changed by %v, want %v", units, want)
	}
	if got := readFile(t, dump); !strings.Contains(got, "the sites' keys") {
		t.Errorf("the data directory's dump was changed: %q", got)
	}
	if !strings.Contains(e.logs.String(), "A file of Zimam's Valkey settings is missing") {
		t.Errorf("the job log doesn't say why Valkey was stopped:\n%s", e.logs.String())
	}
	if _, err := os.Stat(e.p.ValkeyDropIn()); err != nil {
		t.Errorf("the drop-in was not made again: %v", err)
	}
}

func TestRecordedServerWithAFileMissingStaysStoppedWhenTheFilesFail(t *testing.T) {
	t.Run("the candidate refuses them", func(t *testing.T) {
		e := newEnv(t)
		dump := recordedWithoutDropIn(t, e)
		e.add(e.hold()...)
		e.add(aptUpdate, aptInstall, failing(e.throwaway()[0], 1, "*** FATAL CONFIG FILE ERROR ***\n"))
		if _, err := Install(context.Background(), e.d, noUsers); err == nil {
			t.Fatal("Install succeeded")
		}
		if units := unitCalls(lines(e.run.Calls())); !slices.Equal(units, []string{line(mask), line(stop)}) {
			t.Errorf("the service was changed by %v, want it masked and stopped only", units)
		}
		if _, err := os.Stat(dump); err != nil {
			t.Errorf("the dump is gone: %v", err)
		}
		missing(t, e.p.ValkeyDropIn())
		if !strings.Contains(e.logs.String(), "Sites have no Valkey") {
			t.Errorf("the job log doesn't say sites have no Valkey:\n%s", e.logs.String())
		}
	})
	t.Run("the restart fails", func(t *testing.T) {
		e := newEnv(t)
		dump := recordedWithoutDropIn(t, e)
		e.add(e.hold()...)
		e.add(aptUpdate, aptInstall)
		e.add(e.throwaway()...)
		e.add(unmask, reload, enable, failing(restart, 1, "Job failed\n"), reload, stop, mask)
		// Zimam's server saved a dump as it stopped: it holds the sites' keys.
		e.srv.onCall = func(c sysexec.Cmd, _ context.Context) error {
			if c.Path == systemctl && slices.Equal(c.Args, stop.Args) {
				_ = os.WriteFile(dump, []byte("REDIS0012 the sites' keys, saved"), 0o600)
			}
			return nil
		}
		e.d.Run = e.srv
		if _, err := Install(context.Background(), e.d, noUsers); err == nil {
			t.Fatal("Install succeeded")
		}
		units := unitCalls(lines(e.run.Calls()))
		if units[len(units)-1] != line(mask) || countOf(units, line(restart)) != 1 {
			t.Errorf("the service was changed by %v, want it masked at the end and restarted once", units)
		}
		if got := readFile(t, dump); !strings.Contains(got, "saved") {
			t.Errorf("the sites' dump was deleted or changed: %q", got)
		}
		if st, _ := services.Load(e.p); !st.Valkey.Installed {
			t.Error("services.json lost the record of Valkey")
		}
	})
}

// A service that a killed or failed run left masked, with all the files in
// place, is brought back by the next run (see also the table of unconverged
// servers above).
func TestMaskedServerWithAllFilesIsHealed(t *testing.T) {
	e := newEnv(t)
	e.scriptInstall()
	if _, err := Install(context.Background(), e.d, noUsers); err != nil {
		t.Fatal(err)
	}
	before := len(e.run.Calls())
	masked := sysexec.Script{Path: isEnabled.Path, Args: isEnabled.Args, Result: sysexec.Result{ExitCode: 1, Stdout: []byte("masked\n")}}
	e.add(aptUpdate, aptInstall, masked)
	e.add(e.throwaway()...)
	e.add(unmask, enable, restart, livePing, liveCfg, liveACL, liveServer)
	if _, err := Install(context.Background(), e.d, noUsers); err != nil {
		t.Fatal(err)
	}
	units := unitCalls(lines(e.run.Calls()[before:]))
	if want := []string{line(unmask), line(enable), line(restart)}; !slices.Equal(units, want) {
		t.Errorf("the service was changed by %v, want %v", units, want)
	}
}

// The candidate is refused, before the real server is touched, if it loaded
// anything other than what the files say.
func TestCandidateThatLoadedTheWrongThingsIsRefused(t *testing.T) {
	for name, tc := range map[string]struct {
		change func(e *testEnv, s []sysexec.Script) []sysexec.Script
		want   string
	}{
		"the default user is on": {
			change: func(e *testEnv, s []sysexec.Script) []sysexec.Script {
				s[2] = e.sock("user default on nopass ~* &* +@all\nuser zimam-admin on sanitize-payload #abc ~* &* +@all\n", "ACL", "LIST")
				return s[:3]
			},
			want: "default user on",
		},
		"there is no admin": {
			change: func(e *testEnv, s []sysexec.Script) []sysexec.Script {
				s[2] = e.sock("user default off sanitize-payload resetchannels -@all\n", "ACL", "LIST")
				return s[:3]
			},
			want: "no zimam-admin",
		},
		"there is another user": {
			change: func(e *testEnv, s []sysexec.Script) []sysexec.Script {
				s[2] = aclWith("s_other")
				s[2] = e.sock(string(s[2].Result.Stdout), "ACL", "LIST")
				return s[:3]
			},
			want: "has the users",
		},
		"it listens on every address": {
			change: func(e *testEnv, s []sysexec.Script) []sysexec.Script {
				s[3] = e.sock("bind\n* -::*\naclfile\n"+e.p.ValkeyACL()+"\n", "CONFIG", "GET", "bind", "aclfile")
				return s
			},
			want: "is bound to",
		},
		"it reads another ACL file": {
			change: func(e *testEnv, s []sysexec.Script) []sysexec.Script {
				s[3] = e.sock("bind\n127.0.0.1 -::1\naclfile\n/etc/valkey/users.acl\n", "CONFIG", "GET", "bind", "aclfile")
				return s
			},
			want: "reads the ACL file",
		},
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			withPrevious(t, e)
			steps := tc.change(e, e.throwaway())
			e.add(aptUpdate, aptInstall)
			e.add(steps...)
			_, err := Install(context.Background(), e.d, noUsers)
			var tf *configtx.TestFailed
			if !errors.As(err, &tf) || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Install = %v, want a refused candidate (%q)", err, tc.want)
			}
			// The real server was not touched.
			if units := unitCalls(lines(e.run.Calls())); len(units) != 0 {
				t.Errorf("the service was changed by %v", units)
			}
			if got := readFile(t, e.p.ValkeyConf()); got != prevConf {
				t.Errorf("valkey.conf = %q, want the previous file", got)
			}
		})
	}
}

// If staging the new admin password fails after the other files are already
// as wanted, the test's next call is the undo, and must not test the old
// password as if it were the new files.
func TestAnAdminPasswordThatCantBeStagedIsTheUndo(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("a read-only directory stops nobody who is root")
	}
	e := newEnv(t)
	e.scriptInstall()
	if _, err := Install(context.Background(), e.d, noUsers); err != nil {
		t.Fatal(err)
	}
	f := files{conf: []byte(readFile(t, e.p.ValkeyConf())), acl: []byte(readFile(t, e.p.ValkeyACL())),
		dropIn: []byte(readFile(t, e.p.ValkeyDropIn())), adminPass: []byte(strings.Repeat("b", 43) + "\n")}
	if err := os.Chmod(e.p.ValkeyDir(), 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(e.p.ValkeyDir(), 0o750) })
	before := len(e.run.Calls())
	if _, err := applyFiles(context.Background(), e.d, f, target{uid: os.Getuid(), gid: os.Getgid(), memMiB: 102}); err == nil {
		t.Fatal("applyFiles succeeded though nothing could be written")
	}
	if got := lines(e.run.Calls()[before:]); len(got) != 0 {
		t.Errorf("ran %v, want nothing: the first call of the test was the undo", got)
	}
}

func countOf(s []string, v string) int {
	n := 0
	for _, x := range s {
		if x == v {
			n++
		}
	}
	return n
}
