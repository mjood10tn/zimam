//go:build roottest

package mysql

import (
	"bufio"
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"zimam/agent/internal/paths"
	"zimam/agent/internal/services"
	"zimam/agent/internal/sysexec"
)

// This test installs MySQL from the archive onto the machine it runs on, so
// it runs only where that is wanted: a throwaway container, with
// ZIMAM_ROOTTEST_MYSQL=1, as go test -tags roottest ./internal/mysql.

func rootDeps(t *testing.T) sysexec.Deps {
	t.Helper()
	if os.Geteuid() != 0 {
		t.Fatal("the roottest tests need root")
	}
	if os.Getenv("ZIMAM_ROOTTEST_MYSQL") != "1" {
		t.Skip("set ZIMAM_ROOTTEST_MYSQL=1 to install MySQL on this machine")
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
	got := mustQuery(t, d, "SELECT @@bind_address, @@local_infile, @@skip_name_resolve, @@secure_file_priv, @@innodb_buffer_pool_size;")
	t.Logf("live settings: %q", got)
	f := strings.Split(got, "\t")
	if len(f) != 5 || f[0] != "127.0.0.1" || f[1] != "0" || f[2] != "1" || f[3] != "/var/lib/mysql-files/" {
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

	// Running it again converges.
	if _, err := Install(ctx, d); err != nil {
		t.Fatalf("second Install: %v", err)
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
			t.Errorf("%s: 99-zimam.cnf = %q, %v; want the previous file", name, now, rerr)
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
}

func mustMem(t *testing.T, d sysexec.Deps) uint64 {
	t.Helper()
	m, err := memTotal(d)
	if err != nil {
		t.Fatal(err)
	}
	return m
}
