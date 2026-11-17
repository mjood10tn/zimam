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
	"testing"
	"time"

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

// the candidate server's answers: it starts, and answers the admin's PING
// over its socket.
func (e *testEnv) throwaway() []sysexec.Script {
	t := e.p.ValkeyTestDir()
	args := []string{"-u", "valkey", "--", "/usr/bin/valkey-server", e.p.ValkeyConf(), "--aclfile", e.p.ValkeyACL(),
		"--port", "0", "--unixsocket", t + "/s.sock", "--daemonize", "no", "--supervised", "no", "--pidfile", t + "/p",
		"--logfile", "", "--dir", t, "--save", "", "--appendonly", "no"}
	ping := sysexec.Script{Path: cliPath, Args: []string{"-e", "--no-auth-warning", "--user", "zimam-admin", "-s", t + "/s.sock", "PING"}, Result: sysexec.Result{Stdout: []byte("PONG\n")}}
	return []sysexec.Script{{Path: runuser, Args: args}, ping}
}

func (e *testEnv) add(ss ...sysexec.Script) {
	for _, s := range ss {
		e.run.Add(s)
	}
}

// scriptInstall scripts a whole successful install: the packages, the
// candidate server, the reload and restart, and the live checks.
func (e *testEnv) scriptInstall() {
	e.add(aptUpdate, aptInstall)
	e.add(e.throwaway()...)
	e.add(reload, restart, livePing, liveCfg, liveACL, liveServer)
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
		line(aptUpdate),
		line(aptInstall),
		line(e.throwaway()[0]), // the candidate files, tested on a server of their own
		line(e.throwaway()[1]),
		line(reload), // the drop-in is new
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
	for _, c := range calls[:2] {
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
	for _, c := range calls[2:4] {
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
	e.add(aptUpdate, aptInstall)
	e.add(e.throwaway()...)
	e.add(reload, restart, livePing, live(strings.Replace(liveConfig, "106954752", "213909504", 1), "CONFIG", "GET", "bind", "port", "aclfile", "maxmemory", "maxmemory-policy"), liveACL, liveServer)
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
	e.add(aptUpdate, failing(aptInstall, 100, "E: Unable to locate package valkey-server\n"))
	_, err := Install(context.Background(), e.d, noUsers)
	if err == nil || !strings.Contains(err.Error(), "Unable to locate package") {
		t.Fatalf("Install = %v, want apt-get's error", err)
	}
	for _, p := range []string{e.p.ValkeyDir(), e.p.ValkeyDropInDir(), e.p.ServicesState()} {
		missing(t, p)
	}
	if n := len(e.run.Calls()); n != 2 {
		t.Errorf("ran %d commands, want only apt-get update and install", n)
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
	e.add(aptUpdate, aptInstall, livePing, liveCfg, liveACL, liveServer)
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
	for _, f := range []string{e.p.ValkeyConf(), e.p.ValkeyDropIn(), e.p.ValkeyACL(), e.logs.String()} {
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
	e.add(aptUpdate, aptInstall, livePing, liveCfg, liveACL, liveServer)
	if _, err := Install(context.Background(), e.d, noUsers); err != nil {
		t.Fatalf("second Install: %v", err)
	}
	got := lines(e.run.Calls()[before:])
	want := []string{line(aptUpdate), line(aptInstall), line(livePing), line(liveCfg), line(liveACL), line(liveServer)}
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
	for name, tc := range map[string]struct {
		cfg, acl sysexec.Script
		ping     sysexec.Script
	}{
		"the server is down":          {liveCfg, liveACL, failing(livePing, 1, "Could not connect to Valkey at 127.0.0.1:6379: Connection refused\n")},
		"it listens elsewhere":        {live(strings.Replace(liveConfig, "127.0.0.1 -::1", "* -::*", 1), "CONFIG", "GET", "bind", "port", "aclfile", "maxmemory", "maxmemory-policy"), liveACL, livePing},
		"it has another ACL file":     {live(strings.Replace(liveConfig, "/etc/zimam/valkey/users.acl", "/etc/valkey/users.acl", 1), "CONFIG", "GET", "bind", "port", "aclfile", "maxmemory", "maxmemory-policy"), liveACL, livePing},
		"its default user is on":      {liveCfg, live("user default on nopass ~* &* +@all\n", "ACL", "LIST"), livePing},
		"it has another memory limit": {live(strings.Replace(liveConfig, "106954752", "0", 1), "CONFIG", "GET", "bind", "port", "aclfile", "maxmemory", "maxmemory-policy"), liveACL, livePing},
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			e.scriptInstall()
			if _, err := Install(context.Background(), e.d, noUsers); err != nil {
				t.Fatal(err)
			}
			before := len(e.run.Calls())
			// The second run's live checks fail, then the restart's pass.
			e.add(aptUpdate, aptInstall, tc.ping, tc.cfg, tc.acl)
			e.add(e.throwaway()...)
			e.add(restart, livePing, liveCfg, liveACL, liveServer)
			if _, err := Install(context.Background(), e.d, noUsers); err != nil {
				t.Fatalf("second Install: %v", err)
			}
			got := lines(e.run.Calls()[before:])
			if !slices.Contains(got, line(restart)) {
				t.Errorf("the second run never restarted:\n%s", strings.Join(got, "\n"))
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

func TestBadConfigRestoresPrevious(t *testing.T) {
	badConfig := "*** FATAL CONFIG FILE ERROR (Version 9.0.4) ***\n>>> 'maxmemory lots'\n"
	runuserFails := func(e *testEnv) sysexec.Script { return failing(e.throwaway()[0], 1, badConfig) }
	pingBad := func(e *testEnv) sysexec.Script {
		s := e.throwaway()[1]
		return failing(s, 1, "AUTH failed: WRONGPASS invalid username-password pair or user is disabled.\n")
	}
	cfgWith := func(old, new string) sysexec.Script {
		return live(strings.Replace(liveConfig, old, new, 1), "CONFIG", "GET", "bind", "port", "aclfile", "maxmemory", "maxmemory-policy")
	}
	restartFails := failing(restart, 1, "Job for valkey-server.service failed because the control process exited with error code.\n")
	type tc struct {
		script func(e *testEnv) []sysexec.Script // after the packages
		want   string                            // in the error
		calls  func(e *testEnv) []string         // after the packages
		small  bool                              // run with a short wait for the candidate server
	}
	for name, tc := range map[string]tc{
		"the candidate server refuses the file": {
			script: func(e *testEnv) []sysexec.Script { return []sysexec.Script{runuserFails(e)} },
			want:   "FATAL CONFIG FILE ERROR",
			// Nothing restarted or reloaded: the real server never read the file.
			calls: func(e *testEnv) []string { return []string{line(e.throwaway()[0])} },
		},
		"the candidate server never answers": {
			script: func(e *testEnv) []sysexec.Script { return []sysexec.Script{e.throwaway()[0], pingBad(e)} },
			want:   "WRONGPASS",
			calls:  func(e *testEnv) []string { return []string{line(e.throwaway()[0]), line(e.throwaway()[1])} },
			small:  true,
		},
		"systemd can't reload": {
			script: func(e *testEnv) []sysexec.Script {
				return append(e.throwaway(), failing(reload, 1, "Failed to reload daemon\n"), reload, resetFailed, restart)
			},
			want: "Failed to reload",
			calls: func(e *testEnv) []string {
				return []string{line(e.throwaway()[0]), line(e.throwaway()[1]), line(reload), line(reload), line(resetFailed), line(restart)}
			},
		},
		"the restart fails": {
			script: func(e *testEnv) []sysexec.Script {
				return append(e.throwaway(), reload, restartFails, reload, resetFailed, restart)
			},
			want: "Job for valkey-server.service failed",
			// The server starts again from the previous files.
			calls: func(e *testEnv) []string {
				return []string{line(e.throwaway()[0]), line(e.throwaway()[1]), line(reload), line(restart), line(reload), line(resetFailed), line(restart)}
			},
		},
		"the admin can't log in after the restart": {
			script: func(e *testEnv) []sysexec.Script {
				return append(e.throwaway(), reload, restart, failing(livePing, 1, "AUTH failed: WRONGPASS invalid username-password pair\n"), reload, resetFailed, restart)
			},
			want: "WRONGPASS",
			calls: func(e *testEnv) []string {
				return []string{line(e.throwaway()[0]), line(e.throwaway()[1]), line(reload), line(restart), line(livePing), line(reload), line(resetFailed), line(restart)}
			},
		},
		"it listens on every address": {
			script: func(e *testEnv) []sysexec.Script {
				return append(e.throwaway(), reload, restart, livePing, cfgWith("127.0.0.1 -::1", "* -::*"), liveACL, liveServer, reload, resetFailed, restart)
			},
			want: "bind is",
			calls: func(e *testEnv) []string {
				return []string{line(e.throwaway()[0]), line(e.throwaway()[1]), line(reload), line(restart), line(livePing), line(cfgWith("", "")), line(liveACL), line(liveServer), line(reload), line(resetFailed), line(restart)}
			},
		},
		"it has no memory limit": {
			script: func(e *testEnv) []sysexec.Script {
				return append(e.throwaway(), reload, restart, livePing, cfgWith("106954752", "0"), liveACL, liveServer, reload, resetFailed, restart)
			},
			want: "maxmemory is",
			calls: func(e *testEnv) []string {
				return []string{line(e.throwaway()[0]), line(e.throwaway()[1]), line(reload), line(restart), line(livePing), line(cfgWith("", "")), line(liveACL), line(liveServer), line(reload), line(resetFailed), line(restart)}
			},
		},
		"its default user is still on": {
			script: func(e *testEnv) []sysexec.Script {
				return append(e.throwaway(), reload, restart, livePing, liveCfg, live("user default on nopass ~* &* +@all\n", "ACL", "LIST"), liveServer, reload, resetFailed, restart)
			},
			want: "default user",
			calls: func(e *testEnv) []string {
				return []string{line(e.throwaway()[0]), line(e.throwaway()[1]), line(reload), line(restart), line(livePing), line(liveCfg), line(liveACL), line(liveServer), line(reload), line(resetFailed), line(restart)}
			},
		},
	} {
		for _, previous := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s (previous files: %v)", name, previous), func(t *testing.T) {
				if tc.small {
					old := startTimeout
					startTimeout = 300 * time.Millisecond
					t.Cleanup(func() { startTimeout = old })
				}
				e := newEnv(t)
				if previous {
					withPrevious(t, e)
				}
				e.add(aptUpdate, aptInstall)
				e.add(tc.script(e)...)
				_, err := Install(context.Background(), e.d, noUsers)
				if err == nil || !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("Install = %v, want an error holding %q", err, tc.want)
				}
				if strings.Contains(err.Error(), "also") {
					t.Errorf("the restore failed too: %v", err)
				}
				for path, content := range map[string]string{e.p.ValkeyConf(): prevConf, e.p.ValkeyACL(): prevACL, e.p.ValkeyDropIn(): prevDropIn} {
					if !previous {
						missing(t, path)
						continue
					}
					if got := readFile(t, path); got != content {
						t.Errorf("%s =\n%s\nwant the previous file back:\n%s", path, got, content)
					}
					missing(t, path+".zimam-prev")
				}
				got := lines(e.run.Calls())[2:]
				if tc.small {
					got = slices.Compact(got) // the candidate's PING is retried until the wait ends
				}
				want := tc.calls(e)
				if !slices.Equal(got, want) {
					t.Errorf("after the packages ran\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
				}
				missing(t, e.p.ServicesState())
				missing(t, e.p.ValkeyTestDir())
				if e.killed.Load() > 1 {
					t.Errorf("the candidate server was stopped %d times", e.killed.Load())
				}
				if _, err := os.Stat(e.p.ValkeyAdminPass()); err != nil {
					t.Errorf("admin.pass: %v", err)
				}
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
	e.add(reload, restart, reload, resetFailed, restart)
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
	e.add(reload, restart, reload, resetFailed, restart)
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
	e.add(reload, failing(restart, 1, "Job failed\n"), reload, failing(restart, 1, "Job failed again\n"))
	_, err := Install(context.Background(), e.d, noUsers)
	if err == nil || !strings.Contains(err.Error(), "may be running settings that are not in") {
		t.Fatalf("Install = %v, want the warning about the server's settings", err)
	}
}

func TestSiteUsersAreKeptAndSorted(t *testing.T) {
	e := newEnv(t)
	e.scriptInstall()
	a, b := sha256.Sum256([]byte("a password")), sha256.Sum256([]byte("b password"))
	users := func() ([]SiteUser, error) {
		return []SiteUser{{Name: "shop", Hash: b}, {Name: "blog", Hash: a}}, nil
	}
	if _, err := Install(context.Background(), e.d, users); err != nil {
		t.Fatal(err)
	}
	got := readFile(t, e.p.ValkeyACL())
	wantBlog := "user s_blog on #" + hex.EncodeToString(a[:]) + " resetkeys ~s_blog:* resetchannels &s_blog:* +@all -@admin -@dangerous -function\n"
	wantShop := "user s_shop on #" + hex.EncodeToString(b[:]) + " resetkeys ~s_shop:* resetchannels &s_shop:* +@all -@admin -@dangerous -function\n"
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
	e.add(e.throwaway()...)
	e.add(restart, livePing, liveCfg, liveACL, liveServer)
	if _, err := Install(context.Background(), e.d, users); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, e.p.ValkeyACL()); !strings.Contains(got, "user s_blog on #"+hex.EncodeToString(h[:])) {
		t.Errorf("users.acl lost the site user:\n%s", got)
	}
	// And with it in place, the next run changes nothing.
	before := len(e.run.Calls())
	e.add(aptUpdate, aptInstall, livePing, liveCfg, liveACL, liveServer)
	if _, err := Install(context.Background(), e.d, users); err != nil {
		t.Fatal(err)
	}
	if n := len(e.run.Calls()) - before; n != 6 {
		t.Errorf("the run with nothing new made %d calls, want 6", n)
	}
}

func TestSiteUsersFailingChangesNothing(t *testing.T) {
	e := newEnv(t)
	e.add(aptUpdate, aptInstall)
	_, err := Install(context.Background(), e.d, func() ([]SiteUser, error) { return nil, errors.New("shop.json is corrupt") })
	if err == nil || !strings.Contains(err.Error(), "shop.json is corrupt") {
		t.Fatalf("Install = %v", err)
	}
	missing(t, e.p.ValkeyConf())
	missing(t, e.p.ValkeyACL())
	missing(t, e.p.ServicesState())
	if n := len(e.run.Calls()); n != 2 {
		t.Errorf("ran %d commands, want only the packages", n)
	}
}

// Two writers of the ACL file, this install and a site's cache user, take
// one lock, so they never interleave.
func TestInstallWaitsForTheACLLock(t *testing.T) {
	e := newEnv(t)
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
	e.add(reload, failing(restart, 1, "Job failed\n"), reload, failing(resetFailed, 1, "Failed\n"), restart)
	_, err := Install(context.Background(), e.d, noUsers)
	if err == nil || strings.Contains(err.Error(), "may be running") {
		t.Fatalf("Install = %v, want only the first failure", err)
	}
	got := lines(e.run.Calls())
	if got[len(got)-1] != line(restart) || got[len(got)-2] != line(resetFailed) {
		t.Errorf("the last calls were\n%s\nwant reset-failed and then a restart", strings.Join(got[len(got)-3:], "\n"))
	}
}
