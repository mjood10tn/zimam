//go:build roottest

package mysql

import (
	"bufio"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"zimam/agent/internal/metrics"
	"zimam/agent/internal/paths"
	"zimam/agent/internal/services"
	"zimam/agent/internal/sysexec"
)

// This test installs MySQL from the archive onto the machine it runs on, so
// it runs only where that is wanted: a throwaway container, with
// ZIMAM_ROOTTEST_MYSQL=1, as go test -tags roottest ./internal/mysql.

func rootDeps(t *testing.T) sysexec.Deps {
	t.Helper()
	if os.Getenv("ZIMAM_ROOTTEST_MYSQL") != "1" {
		t.Skip("set ZIMAM_ROOTTEST_MYSQL=1 to install MySQL on this machine")
	}
	if os.Geteuid() != 0 {
		t.Fatal("the roottest tests need root")
	}
	return sysexec.Deps{Paths: paths.Paths{}, Run: sysexec.ExecRunner{}, Logf: t.Logf, Now: time.Now}
}

// listeners returns the local addresses, as "address:port" in hex, of the
// TCP sockets that listen on port.
func listeners(t *testing.T, port uint16) []string {
	t.Helper()
	var found []string
	for _, file := range []string{"/proc/net/tcp", "/proc/net/tcp6"} {
		f, err := os.Open(file)
		if err != nil {
			t.Fatal(err)
		}
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			fields := strings.Fields(sc.Text())
			// local_address, rem_address, st (0A is LISTEN)
			if len(fields) < 4 || fields[3] != "0A" {
				continue
			}
			addr, p, _ := strings.Cut(fields[1], ":")
			if p == hex(port) {
				found = append(found, addr)
			}
		}
		f.Close()
	}
	return found
}

func hex(p uint16) string {
	const digits = "0123456789ABCDEF"
	return string([]byte{digits[p>>12&15], digits[p>>8&15], digits[p>>4&15], digits[p&15]})
}

func isActive(t *testing.T) bool {
	t.Helper()
	return exec.Command("/usr/bin/systemctl", "is-active", "--quiet", "mysql").Run() == nil
}

func mustQuery(t *testing.T, d sysexec.Deps, sql string) string {
	t.Helper()
	out, err := query(context.Background(), d, sql)
	if err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return strings.TrimSpace(out)
}

func TestRootMySQLInstall(t *testing.T) {
	d := rootDeps(t)
	ctx := context.Background()

	version, err := Install(ctx, d)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	t.Logf("installed MySQL %s", version)
	st, err := services.Load(d.Paths)
	if err != nil || st.MySQL != (services.Entry{Installed: true, Version: version}) {
		t.Fatalf("services.json: %+v, %v", st, err)
	}
	conf, err := os.ReadFile(d.Paths.MySQLConf())
	if err != nil {
		t.Fatal(err)
	}

	// The settings are live, read again here and not only by Install.
	got := mustQuery(t, d, "SELECT @@bind_address, @@local_infile, @@skip_name_resolve, @@secure_file_priv, @@innodb_buffer_pool_size, @@log_bin;")
	t.Logf("live settings: %q", got)
	f := strings.Split(got, "\t")
	if len(f) != 6 || f[0] != "127.0.0.1" || f[1] != "0" || f[2] != "1" || f[3] != "/var/lib/mysql-files/" || f[5] != "0" {
		t.Errorf("live settings = %q", got)
	}
	if user := mustQuery(t, d, "SELECT CURRENT_USER();"); user != "root@localhost" {
		t.Errorf("root connects as %q", user)
	}
	if plugin := mustQuery(t, d, rootPluginQuery); plugin != "auth_socket" {
		t.Errorf("root@localhost uses %q", plugin)
	}

	// Only loopback listens, and the X protocol port is closed.
	if got := listeners(t, 3306); len(got) != 1 || got[0] != "0100007F" {
		t.Errorf("port 3306 listens on %v, want only 127.0.0.1 (0100007F)", got)
	}
	if got := listeners(t, 33060); len(got) != 0 {
		t.Errorf("port 33060 listens on %v, want it closed", got)
	}

	// Running it again converges, without restarting a server that already
	// runs with the file.
	pidBefore := mainPID(t)
	if _, err := Install(ctx, d); err != nil {
		t.Fatalf("second Install: %v", err)
	}
	if pidAfter := mainPID(t); pidAfter != pidBefore {
		t.Errorf("MySQL restarted on a re-run that changed nothing: pid %s, then %s", pidBefore, pidAfter)
	}

	// Each kind of bad file is refused, the previous file comes back and the
	// server runs with the settings it had.
	pool := BufferPoolMiB(mustMem(t, d))
	for name, bad := range map[string]string{
		"an unknown option":        "[mysqld]\nnot_an_option = 1\n",
		"a broken group header":    "[mysqld\nbind-address = 127.0.0.1\n",
		"a value that starts fine": strings.Replace(string(conf), "local_infile = OFF", "local_infile = ON", 1),
		"the X plugin left on":     strings.Replace(string(conf), "mysqlx = OFF", "mysqlx = ON", 1),
	} {
		_, err := applyConfig(ctx, d, []byte(bad), pool)
		t.Logf("%s: %v", name, err)
		if err == nil {
			t.Errorf("%s: applyConfig accepted it", name)
		}
		if now, rerr := os.ReadFile(d.Paths.MySQLConf()); rerr != nil || string(now) != string(conf) {
			t.Errorf("%s: zz-zimam.cnf = %q, %v; want the previous file", name, now, rerr)
		}
		if !isActive(t) {
			t.Fatalf("%s: MySQL is not running after the rollback", name)
		}
		if got := mustQuery(t, d, "SELECT @@bind_address, @@local_infile, @@skip_name_resolve;"); got != "127.0.0.1\t0\t1" {
			t.Errorf("%s: live settings after the rollback = %q", name, got)
		}
		if got := listeners(t, 3306); len(got) != 1 || got[0] != "0100007F" {
			t.Errorf("%s: port 3306 listens on %v after the rollback", name, got)
		}
		if got := listeners(t, 33060); len(got) != 0 {
			t.Errorf("%s: port 33060 listens on %v after the rollback", name, got)
		}
	}

	// A root account that was given a password is put back on auth_socket.
	mustQuery(t, d, "ALTER USER 'root'@'localhost' IDENTIFIED WITH caching_sha2_password BY '';")
	if _, err := Install(ctx, d); err != nil {
		t.Fatalf("Install with root on a password plugin: %v", err)
	}
	if plugin := mustQuery(t, d, rootPluginQuery); plugin != "auth_socket" {
		t.Errorf("root@localhost uses %q after Install", plugin)
	}

	// The package's own mysqld.cnf sorts before our file, so our value wins
	// over a conflicting one there.
	pkgConf := "/etc/mysql/mysql.conf.d/mysqld.cnf"
	orig, err := os.ReadFile(pkgConf)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.WriteFile(pkgConf, orig, 0o644) })
	if !strings.Contains(string(orig), "bind-address") {
		t.Fatal("mysqld.cnf has no bind-address line to conflict with")
	}
	conflict := regexp.MustCompile(`(?m)^bind-address.*$`).ReplaceAllString(string(orig), "bind-address = 0.0.0.0")
	if err := os.WriteFile(pkgConf, []byte(conflict), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Install(ctx, d); err != nil {
		t.Fatalf("Install with a conflicting value in mysqld.cnf: %v", err)
	}
	if got := mustQuery(t, d, "SELECT @@bind_address;"); got != "127.0.0.1" {
		t.Errorf("bind_address = %q with a conflicting mysqld.cnf, want ours (127.0.0.1)", got)
	}
	if got := listeners(t, 3306); len(got) != 1 || got[0] != "0100007F" {
		t.Errorf("port 3306 listens on %v with a conflicting mysqld.cnf", got)
	}
	if err := os.WriteFile(pkgConf, orig, 0o644); err != nil {
		t.Fatal(err)
	}

	// A file that sorts after ours and overrides one of our settings is
	// caught by the live check: the install fails and leaves the previous
	// state.
	other := "/etc/mysql/mysql.conf.d/zzz-other.cnf"
	if err := os.WriteFile(other, []byte("[mysqld]\nlocal_infile = ON\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Remove(other) })
	before, err := os.ReadFile(d.Paths.MySQLConf())
	if err != nil {
		t.Fatal(err)
	}
	// The running server has not read the new file yet, so its values still
	// match and nothing restarts; once it restarts, the override shows.
	if out, err := exec.Command("/usr/bin/systemctl", "restart", "mysql").CombinedOutput(); err != nil {
		t.Fatalf("restart: %v\n%s", err, out)
	}
	if _, err := Install(ctx, d); err == nil || !strings.Contains(err.Error(), "local_infile") {
		t.Fatalf("Install with an overriding zzz-other.cnf = %v, want a local_infile failure", err)
	}
	if after, _ := os.ReadFile(d.Paths.MySQLConf()); string(after) != string(before) {
		t.Errorf("zz-zimam.cnf changed after the failed install:\n%s", after)
	}
	if !isActive(t) {
		t.Fatal("MySQL is not running after the failed install")
	}
	os.Remove(other)
	if _, err := Install(ctx, d); err != nil {
		t.Fatalf("Install after removing zzz-other.cnf: %v", err)
	}
	if got := mustQuery(t, d, "SELECT @@local_infile;"); got != "0" {
		t.Errorf("local_infile = %q after converging", got)
	}

	// A root account with a password refuses the socket login: Install says
	// so before it touches the file or restarts MySQL.
	mustQuery(t, d, "ALTER USER 'root'@'localhost' IDENTIFIED WITH caching_sha2_password BY 'zimam-roottest-pw';")
	confBefore, err := os.ReadFile(d.Paths.MySQLConf())
	if err != nil {
		t.Fatal(err)
	}
	pid := mainPID(t)
	_, err = Install(ctx, d)
	if err == nil || !strings.Contains(err.Error(), "auth_socket") || !strings.Contains(err.Error(), "password") {
		t.Errorf("Install with a root password = %v, want the clear refusal", err)
	}
	t.Logf("with a root password: %v", err)
	if after, _ := os.ReadFile(d.Paths.MySQLConf()); string(after) != string(confBefore) {
		t.Errorf("the config file changed:\n%s", after)
	}
	if got := mainPID(t); got != pid {
		t.Errorf("MySQL restarted: pid %s, then %s", pid, got)
	}
	// Put root back, logging in with the password as an administrator would.
	restore := exec.Command("/usr/bin/mysql", "--protocol=socket", "--user=root", "--password=zimam-roottest-pw", "--batch", "--skip-column-names",
		"-e", "ALTER USER 'root'@'localhost' IDENTIFIED WITH auth_socket;")
	if out, err := restore.CombinedOutput(); err != nil {
		t.Fatalf("restoring root: %v\n%s", err, out)
	}
	if plugin := mustQuery(t, d, rootPluginQuery); plugin != "auth_socket" {
		t.Errorf("root@localhost uses %q after restoring it", plugin)
	}
}

func mainPID(t *testing.T) string {
	t.Helper()
	out, err := exec.Command("/usr/bin/systemctl", "show", "-p", "MainPID", "--value", "mysql").Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(out))
}

func mustMem(t *testing.T, d sysexec.Deps) uint64 {
	t.Helper()
	m, err := metrics.MemTotal(d.Paths)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// An install over a server that writes binary logs, as MySQL does by
// default, turns them off and converges.
func TestRootMySQLBinaryLogsTurnedOff(t *testing.T) {
	d := rootDeps(t)
	ctx := context.Background()
	if _, err := Install(ctx, d); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if got := mustQuery(t, d, "SELECT @@log_bin;"); got != "0" {
		t.Fatalf("log_bin = %q after Install, want 0", got)
	}
	// Back to a package-default server: no Zimam file, binary logs on.
	if err := os.Remove(d.Paths.MySQLConf()); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("/usr/bin/systemctl", "restart", "mysql").CombinedOutput(); err != nil {
		t.Fatalf("restart: %v\n%s", err, out)
	}
	if got := mustQuery(t, d, "SELECT @@log_bin;"); got != "1" {
		t.Fatalf("log_bin = %q on the package's own settings, want 1", got)
	}
	logs, _ := filepath.Glob("/var/lib/mysql/binlog.*")
	if len(logs) == 0 {
		t.Fatal("the server with binary logs on wrote no binlog files")
	}
	if _, err := Install(ctx, d); err != nil {
		t.Fatalf("Install over a server with binary logs: %v", err)
	}
	if got := mustQuery(t, d, "SELECT @@log_bin;"); got != "0" {
		t.Errorf("log_bin = %q after Install, want 0", got)
	}
	// And again, without a restart.
	pid := mainPID(t)
	if _, err := Install(ctx, d); err != nil {
		t.Fatalf("second Install: %v", err)
	}
	if got := mainPID(t); got != pid {
		t.Errorf("MySQL restarted on a re-run that changed nothing: pid %s, then %s", pid, got)
	}
}
