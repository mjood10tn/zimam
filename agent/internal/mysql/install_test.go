package mysql

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"zimam/agent/internal/paths"
	"zimam/agent/internal/services"
	"zimam/agent/internal/sysexec"
)

const (
	gib = 1 << 30

	lockTimeout = "DPkg::Lock::Timeout=300"

	// what the live query answers on a server that runs Zimam's file with a
	// 256M buffer pool: bind_address, local_infile, skip_name_resolve,
	// secure_file_priv (MySQL adds the slash), innodb_buffer_pool_size,
	// the mysqlx plugin's status and the version.
	liveGood = "127.0.0.1\t0\t1\t/var/lib/mysql-files/\t268435456\tDISABLED\t8.4.13-0ubuntu0.26.04.2\n"
)

var (
	aptUpdate  = sysexec.Script{Path: "/usr/bin/apt-get", Args: []string{"-o", lockTimeout, "update"}}
	aptInstall = sysexec.Script{Path: "/usr/bin/apt-get", Args: []string{"-o", lockTimeout, "-o", "Dpkg::Options::=--force-confdef", "-o", "Dpkg::Options::=--force-confold",
		"install", "-y", "--no-install-recommends", "mysql-server"}}
	validate = sysexec.Script{Path: mysqld, Args: []string{"--validate-config"}}
	restart  = sysexec.Script{Path: systemctl, Args: []string{"restart", "mysql"}}
)

func client(out string) sysexec.Script {
	return sysexec.Script{Path: clientPath, Args: clientArgs, Result: sysexec.Result{Stdout: []byte(out)}}
}

func failing(s sysexec.Script, code int, stderr string) sysexec.Script {
	s.Result = sysexec.Result{ExitCode: code, Stderr: []byte(stderr)}
	return s
}

// testEnv is a server with MySQL's packages unpacked (the config directory
// exists) and 1 GiB of memory, under a temporary root.
type testEnv struct {
	d   sysexec.Deps
	run *sysexec.FakeRunner
	p   paths.Paths
}

func newEnv(t *testing.T, memTotalKiB int) *testEnv {
	t.Helper()
	p := paths.Paths{Root: t.TempDir()}
	mkdir(t, filepath.Dir(p.MySQLConf()))
	mkdir(t, filepath.Dir(p.ProcMeminfo()))
	meminfo := fmt.Sprintf("MemTotal:       %d kB\nMemFree:         100 kB\nMemAvailable:    900 kB\nSwapTotal:       0 kB\nSwapFree:        0 kB\n", memTotalKiB)
	if err := os.WriteFile(p.ProcMeminfo(), []byte(meminfo), 0o644); err != nil {
		t.Fatal(err)
	}
	run := sysexec.NewFakeRunner()
	return &testEnv{d: sysexec.Deps{Paths: p, Run: run, Logf: t.Logf, Now: time.Now}, run: run, p: p}
}

// scriptInstall scripts a whole successful install: the packages, the test
// of the file, the restart, the live values, and root on auth_socket.
func (e *testEnv) scriptInstall() {
	for _, s := range []sysexec.Script{aptUpdate, aptInstall, validate, restart, client(liveGood), client("auth_socket\n")} {
		e.run.Add(s)
	}
}

func mkdir(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
}

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

const wantConf = `# Managed by Zimam. Changes here are overwritten.
[mysqld]
bind-address = 127.0.0.1
mysqlx = OFF
local_infile = OFF
skip_name_resolve = ON
secure_file_priv = /var/lib/mysql-files
innodb_buffer_pool_size = 256M
`

func TestInstallSequence(t *testing.T) {
	e := newEnv(t, gib/1024)
	e.scriptInstall()
	version, err := Install(context.Background(), e.d)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if version != "8.4.13" {
		t.Errorf("version = %q, want the package's upstream version 8.4.13", version)
	}
	want := []string{
		line(aptUpdate),
		line(aptInstall),
		line(validate), // the file's test
		line(restart),  // which has to start with the file
		line(client("")),
		line(client("")), // root's plugin
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
	if got := readFile(t, e.p.MySQLConf()); got != wantConf {
		t.Errorf("99-zimam.cnf =\n%s\nwant\n%s", got, wantConf)
	}
	if fi, err := os.Stat(e.p.MySQLConf()); err != nil || fi.Mode().Perm() != 0o644 {
		t.Errorf("99-zimam.cnf: %v, %v; want mode 0644", fi, err)
	}
	// SQL goes only on stdin. The live query reads exactly the settings that
	// were rendered.
	live := string(calls[4].Stdin)
	for _, w := range []string{"@@bind_address", "@@local_infile", "@@skip_name_resolve", "@@secure_file_priv", "@@innodb_buffer_pool_size", "mysqlx", "@@version"} {
		if !strings.Contains(live, w) {
			t.Errorf("the live query lacks %s:\n%s", w, live)
		}
	}
	if auth := string(calls[5].Stdin); !strings.Contains(auth, "mysql.user") {
		t.Errorf("the plugin query = %q", auth)
	}
	st, err := services.Load(e.p)
	if err != nil {
		t.Fatal(err)
	}
	if st.MySQL != (services.Entry{Installed: true, Version: "8.4.13"}) {
		t.Errorf("services.json says %+v", st.MySQL)
	}
	if st.Valkey != (services.Entry{}) {
		t.Errorf("valkey's entry = %+v, want none", st.Valkey)
	}
}

func TestInstallSetsAuthSocketWhenRootUsesAPassword(t *testing.T) {
	e := newEnv(t, gib/1024)
	for _, s := range []sysexec.Script{aptUpdate, aptInstall, validate, restart, client(liveGood), client("caching_sha2_password\n"), client("")} {
		e.run.Add(s)
	}
	if _, err := Install(context.Background(), e.d); err != nil {
		t.Fatalf("Install: %v", err)
	}
	calls := e.run.Calls()
	if len(calls) != 7 {
		t.Fatalf("ran %d commands, want 7:\n%s", len(calls), strings.Join(lines(calls), "\n"))
	}
	if got, want := string(calls[6].Stdin), "ALTER USER 'root'@'localhost' IDENTIFIED WITH auth_socket;\n"; got != want {
		t.Errorf("the last statement = %q, want %q", got, want)
	}
}

func TestInstallConverges(t *testing.T) {
	e := newEnv(t, gib/1024)
	e.scriptInstall()
	if _, err := Install(context.Background(), e.d); err != nil {
		t.Fatalf("first Install: %v", err)
	}
	e.scriptInstall() // the second run's answers
	// A second run finds everything in place: apt-get finds the packages
	// installed, and the file, its test and the state come out the same.
	if _, err := Install(context.Background(), e.d); err != nil {
		t.Fatalf("second Install: %v", err)
	}
	if got := readFile(t, e.p.MySQLConf()); got != wantConf {
		t.Errorf("99-zimam.cnf after the second run =\n%s", got)
	}
	if n := len(lines(e.run.Calls())); n != 12 {
		t.Errorf("two runs made %d calls, want 12", n)
	}
	st, err := services.Load(e.p)
	if err != nil || st.MySQL.Version != "8.4.13" {
		t.Errorf("services.json: %+v, %v", st, err)
	}
	entries, _ := os.ReadDir(filepath.Dir(e.p.MySQLConf()))
	if len(entries) != 1 {
		t.Errorf("the config directory holds %d entries, want only 99-zimam.cnf", len(entries))
	}
}

func TestInstallAptFailureWritesNothing(t *testing.T) {
	e := newEnv(t, gib/1024)
	e.run.Add(aptUpdate)
	e.run.Add(failing(aptInstall, 100, "E: Unable to locate package mysql-server\n"))
	_, err := Install(context.Background(), e.d)
	if err == nil || !strings.Contains(err.Error(), "Unable to locate package") {
		t.Fatalf("Install = %v, want apt-get's error", err)
	}
	if _, err := os.Lstat(e.p.MySQLConf()); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("99-zimam.cnf was written (Lstat: %v)", err)
	}
	if _, err := os.Lstat(e.p.ServicesState()); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("services.json was written (Lstat: %v)", err)
	}
	if n := len(e.run.Calls()); n != 2 {
		t.Errorf("ran %d commands, want only apt-get update and install", n)
	}
}

func TestBufferPoolFromMemTotal(t *testing.T) {
	for name, tc := range map[string]struct {
		mem  uint64
		want int // MiB
	}{
		"1 GiB gives 256M":                   {1 * gib, 256},
		"64 GiB is capped at 4G":             {64 * gib, 4096},
		"16 GiB is exactly the cap":          {16 * gib, 4096},
		"a quarter of 20 GiB is capped":      {20 * gib, 4096},
		"256 MiB is raised to the floor":     {256 << 20, 128},
		"nothing is raised to the floor":     {0, 128},
		"3 GiB gives 768M":                   {3 * gib, 768},
		"a partial MiB is dropped":           {1*gib + 3<<20 + 1000, 256},
		"a 1 GiB machine's real MemTotal":    {(1 * gib) - (60 << 20), 241},
		"just under the floor stays at 128M": {511 << 20, 128},
	} {
		if got := BufferPoolMiB(tc.mem); got != tc.want {
			t.Errorf("%s: BufferPoolMiB(%d) = %d, want %d", name, tc.mem, got, tc.want)
		}
	}
}

func TestInstallRendersBufferPoolFromMemory(t *testing.T) {
	e := newEnv(t, 64*gib/1024)
	live := "127.0.0.1\t0\t1\t/var/lib/mysql-files/\t4294967296\tDISABLED\t8.4.13-0ubuntu0.26.04.2\n"
	for _, s := range []sysexec.Script{aptUpdate, aptInstall, validate, restart, client(live), client("auth_socket\n")} {
		e.run.Add(s)
	}
	if _, err := Install(context.Background(), e.d); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if got := readFile(t, e.p.MySQLConf()); !strings.Contains(got, "innodb_buffer_pool_size = 4096M\n") {
		t.Errorf("99-zimam.cnf:\n%s", got)
	}
}

// MySQL rounds the pool up to a whole number of chunks times instances, so
// the live value may be above what the file asks for, never below.
func TestInstallAcceptsARoundedBufferPool(t *testing.T) {
	for name, tc := range map[string]struct {
		live string
		ok   bool
	}{
		"equal":                {"268435456", true},
		"rounded up one chunk": {"402653184", true},
		"rounded up by a GiB":  {"1342177280", false},
		"below":                {"134217728", false},
	} {
		e := newEnv(t, gib/1024)
		live := "127.0.0.1\t0\t1\t/var/lib/mysql-files/\t" + tc.live + "\tDISABLED\t8.4.13-0ubuntu0.26.04.2\n"
		for _, s := range []sysexec.Script{aptUpdate, aptInstall, validate, restart, client(live), client("auth_socket\n")} {
			e.run.Add(s)
		}
		_, err := Install(context.Background(), e.d)
		if (err == nil) != tc.ok {
			t.Errorf("%s: Install = %v, want ok = %v", name, err, tc.ok)
		}
	}
}

// previous is a file a server already has, which a failed change must bring
// back.
const previous = "[mysqld]\nbind-address = 127.0.0.1\n"

func withPrevious(t *testing.T, e *testEnv) {
	t.Helper()
	if err := os.WriteFile(e.p.MySQLConf(), []byte(previous), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestBadConfigRestoresPrevious(t *testing.T) {
	bad := "unknown variable 'not_an_option=1'."
	for name, tc := range map[string]struct {
		setup   func(e *testEnv)
		want    string   // in the error
		calls   []string // after the packages
		wantErr string
	}{
		"validate fails": {
			setup: func(e *testEnv) {
				e.run.Add(failing(validate, 1, "2026-11-17T10:00:00Z 0 [ERROR] [MY-000067] [Server] "+bad+"\n"))
			},
			want: bad,
			// Nothing restarted: the server never read the file.
			calls: []string{line(validate), line(validate)},
		},
		"validate exits 0 but reports an error": {
			setup: func(e *testEnv) {
				e.run.Add(sysexec.Script{Path: mysqld, Args: validate.Args, Result: sysexec.Result{Stderr: []byte("mysqld: [ERROR] Wrong group definition in config file /etc/mysql/mysql.conf.d/99-zimam.cnf at line 1.\n")}})
				e.run.Add(validate)
			},
			want:  "Wrong group definition",
			calls: []string{line(validate), line(validate)},
		},
		"restart fails": {
			setup: func(e *testEnv) {
				e.run.Add(validate)
				e.run.Add(failing(restart, 1, "Job for mysql.service failed.\n"))
				e.run.Add(restart)
			},
			want: "Job for mysql.service failed",
			// The server is started again with the previous file.
			calls: []string{line(validate), line(restart), line(validate), line(restart)},
		},
		"MySQL does not answer": {
			setup: func(e *testEnv) {
				e.run.Add(validate)
				e.run.Add(restart)
				e.run.Add(failing(client(""), 1, "ERROR 2002 (HY000): Can't connect to local MySQL server through socket\n"))
			},
			want:  "Can't connect",
			calls: []string{line(validate), line(restart), line(client("")), line(validate), line(restart)},
		},
		"a live value differs": {
			setup: func(e *testEnv) {
				e.run.Add(validate)
				e.run.Add(restart)
				e.run.Add(client("0.0.0.0\t0\t1\t/var/lib/mysql-files/\t268435456\tDISABLED\t8.4.13-0ubuntu0.26.04.2\n"))
			},
			want:  "bind_address",
			calls: []string{line(validate), line(restart), line(client("")), line(validate), line(restart)},
		},
		"the X plugin is still active": {
			setup: func(e *testEnv) {
				e.run.Add(validate)
				e.run.Add(restart)
				e.run.Add(client("127.0.0.1\t0\t1\t/var/lib/mysql-files/\t268435456\tACTIVE\t8.4.13-0ubuntu0.26.04.2\n"))
			},
			want:  "mysqlx",
			calls: []string{line(validate), line(restart), line(client("")), line(validate), line(restart)},
		},
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t, gib/1024)
			withPrevious(t, e)
			e.run.Add(aptUpdate)
			e.run.Add(aptInstall)
			tc.setup(e)
			// The restore's restart, when the setup left it unscripted.
			e.run.Add(restart)
			_, err := Install(context.Background(), e.d)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Install = %v, want an error holding %q", err, tc.want)
			}
			if got := readFile(t, e.p.MySQLConf()); got != previous {
				t.Errorf("99-zimam.cnf =\n%s\nwant the previous file back:\n%s", got, previous)
			}
			if got := lines(e.run.Calls())[2:]; !slices.Equal(got, tc.calls) {
				t.Errorf("after the packages ran\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(tc.calls, "\n"))
			}
			if _, err := os.Lstat(e.p.ServicesState()); !errors.Is(err, fs.ErrNotExist) {
				t.Errorf("services.json was written for a failed install (Lstat: %v)", err)
			}
			if entries, _ := os.ReadDir(filepath.Dir(e.p.MySQLConf())); len(entries) != 1 {
				t.Errorf("the config directory holds %d entries, want only 99-zimam.cnf", len(entries))
			}
		})
	}
}

func TestBadConfigWithoutPreviousFileRemovesOurs(t *testing.T) {
	e := newEnv(t, gib/1024)
	e.run.Add(aptUpdate)
	e.run.Add(aptInstall)
	e.run.Add(validate)
	e.run.Add(restart)
	e.run.Add(client("0.0.0.0\t0\t1\t/var/lib/mysql-files/\t268435456\tDISABLED\t8.4.13-0ubuntu0.26.04.2\n"))
	if _, err := Install(context.Background(), e.d); err == nil {
		t.Fatal("Install succeeded with a live value that differs")
	}
	if _, err := os.Lstat(e.p.MySQLConf()); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("99-zimam.cnf is still there (Lstat: %v)", err)
	}
}

// hangingRestart answers the first restart only when its context ends, as
// systemctl restart does while a unit with an infinite start timeout never
// finishes starting.
type hangingRestart struct {
	sysexec.Runner
	mu    sync.Mutex
	hung  bool
	hangs int
}

func (h *hangingRestart) Run(ctx context.Context, c sysexec.Cmd) (sysexec.Result, error) {
	if c.Path == systemctl && slices.Equal(c.Args, restart.Args) {
		h.mu.Lock()
		first := !h.hung
		h.hung = true
		h.mu.Unlock()
		if first {
			h.hangs++
			<-ctx.Done()
			return sysexec.Result{}, ctx.Err()
		}
	}
	return h.Runner.Run(ctx, c)
}

func TestHangingRestartIsAFailureAndRestoresPrevious(t *testing.T) {
	old := restartTimeout
	restartTimeout = 100 * time.Millisecond
	t.Cleanup(func() { restartTimeout = old })

	e := newEnv(t, gib/1024)
	withPrevious(t, e)
	for _, s := range []sysexec.Script{aptUpdate, aptInstall, validate, restart} {
		e.run.Add(s)
	}
	h := &hangingRestart{Runner: e.run}
	e.d.Run = h
	_, err := Install(context.Background(), e.d)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Install = %v, want the restart's timeout", err)
	}
	if h.hangs != 1 {
		t.Errorf("the restart hung %d times, want once", h.hangs)
	}
	if got := readFile(t, e.p.MySQLConf()); got != previous {
		t.Errorf("99-zimam.cnf =\n%s\nwant the previous file back", got)
	}
	got := lines(e.run.Calls())[2:]
	want := []string{line(validate), line(validate), line(restart)} // the hung restart never reached the fake
	if !slices.Equal(got, want) {
		t.Errorf("ran\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// A test that runs again after a restore must not compare live values with
// the candidate file that was just taken out.
func TestRestoreDoesNotCompareLiveValues(t *testing.T) {
	e := newEnv(t, gib/1024)
	withPrevious(t, e)
	e.run.Add(aptUpdate)
	e.run.Add(aptInstall)
	e.run.Add(validate)
	e.run.Add(restart)
	e.run.Add(client("0.0.0.0\t0\t1\t/var/lib/mysql-files/\t268435456\tDISABLED\t8.4.13-0ubuntu0.26.04.2\n"))
	_, err := Install(context.Background(), e.d)
	if err == nil || strings.Contains(err.Error(), "also fails") {
		t.Fatalf("Install = %v, want only the first failure", err)
	}
}

func TestInstallRefusesAnUnreadableMeminfo(t *testing.T) {
	e := newEnv(t, gib/1024)
	if err := os.WriteFile(e.p.ProcMeminfo(), []byte("MemTotal: lots\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	e.run.Add(aptUpdate)
	e.run.Add(aptInstall)
	if _, err := Install(context.Background(), e.d); err == nil {
		t.Fatal("Install ran with a meminfo it can't read")
	}
	if _, err := os.Lstat(e.p.MySQLConf()); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("99-zimam.cnf was written (Lstat: %v)", err)
	}
}
