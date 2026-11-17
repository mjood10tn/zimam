// Package mysql installs MySQL from Ubuntu's archive and talks to it as
// MySQL's root over the local socket (v0.3.0). Every statement is built here
// from constants and validated values.
package mysql

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"zimam/agent/contract"
	"zimam/agent/internal/configtx"
	"zimam/agent/internal/metrics"
	"zimam/agent/internal/php"
	"zimam/agent/internal/services"
	"zimam/agent/internal/sysexec"
	"zimam/agent/internal/templates"
)

const (
	packageName = "mysql-server"
	unit        = "mysql"
	mysqld      = "/usr/sbin/mysqld"
	systemctl   = "/usr/bin/systemctl"
)

// restartTimeout bounds the restart that makes MySQL read Zimam's file.
// The unit has TimeoutSec=infinity, so a server that can't start would keep
// systemctl waiting for as long as the job may run.
//
// The job may run 900 s. Apart from apt-get (which may wait up to 300 s for
// a dpkg lock, then downloads and installs), the worst case is the root
// check (30 s), the candidate's restart (2 min) and live query (30 s), and
// the restore's validate and restart (2 min plus 30 s): about 6 minutes,
// which leaves about 9 minutes for apt-get.
var restartTimeout = 2 * time.Minute

// The statements Install runs. The first reads what the running server
// uses, and the plugin's status shows whether the X protocol is off, since
// mysqlx is a plugin option and not a variable.
const (
	liveQuery = "SELECT @@bind_address, @@local_infile, @@skip_name_resolve, @@secure_file_priv, @@innodb_buffer_pool_size, " +
		"(SELECT COALESCE(MAX(PLUGIN_STATUS), '') FROM information_schema.PLUGINS WHERE PLUGIN_NAME = 'mysqlx'), @@version;"
	rootPluginQuery = "SELECT plugin FROM mysql.user WHERE user = 'root' AND host = 'localhost';"
	rootAuthSocket  = "ALTER USER 'root'@'localhost' IDENTIFIED WITH auth_socket;"
)

// notApplied is logged when the packages are in but Zimam's settings are not.
const notApplied = "MySQL is installed with the package's own settings and is not recorded as installed yet; running the install again continues from here."

// Install installs MySQL from Ubuntu's archive, writes and tests Zimam's
// settings, makes sure root logs in over the socket, and records the
// install in services.json. It returns the version installed. Running it
// again converges: installed packages are no error, and the file is
// rendered, tested and loaded again.
func Install(ctx context.Context, d sysexec.Deps) (string, error) {
	mem, err := memTotal(d)
	if err != nil {
		return "", err
	}
	d.Logf("Installing MySQL")
	if err := php.InstallPackages(ctx, d, packageName); err != nil {
		return "", err
	}
	// Root must log in over the socket before anything else changes: a root
	// password would otherwise fail the live query and look like a bad file.
	if err := ensureRootAuthSocket(ctx, d); err != nil {
		d.Logf("%s", notApplied)
		return "", err
	}
	pool := BufferPoolMiB(mem)
	conf, err := templates.Render("mysql/zz-zimam.cnf", templates.MySQLConf{BufferPoolMiB: pool})
	if err != nil {
		return "", err
	}
	d.Logf("Writing MySQL's settings (a %d MiB buffer pool)", pool)
	live, err := applyConfig(ctx, d, conf, pool)
	if err != nil {
		d.Logf("%s", notApplied)
		return "", err
	}
	version, _, _ := strings.Cut(live.version, "-")
	d.Logf("MySQL %s is running", version)
	if err := services.Update(d.Paths, func(s *services.State) { s.MySQL = services.Entry{Installed: true, Version: version} }); err != nil {
		return "", err
	}
	return version, nil
}

// BufferPoolMiB is the InnoDB buffer pool for a server with this much
// memory: a quarter of it in whole MiB, at least 128 and at most 4096.
func BufferPoolMiB(memTotalBytes uint64) int {
	return int(min(max(memTotalBytes/4>>20, 128), 4096))
}

func memTotal(d sysexec.Deps) (uint64, error) {
	b, err := os.ReadFile(d.Paths.ProcMeminfo())
	if err != nil {
		return 0, err
	}
	m, err := metrics.ParseMeminfo(b)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", d.Paths.ProcMeminfo(), err)
	}
	return m.TotalBytes, nil
}

// liveValues are what the running server reports.
type liveValues struct {
	bindAddress, localInfile, skipNameResolve, secureFilePriv string
	bufferPoolBytes                                           uint64
	mysqlx, version                                           string
}

// applyConfig writes conf as Zimam's drop-in and keeps it only if the server
// runs with it. The test, in order: mysqld --validate-config must exit 0
// with no [ERROR] on stderr (it exits 0 for a broken group header, which it
// only reports there); MySQL restarts within a time limit; and the live
// values equal what the file asks for. If any step fails the previous file
// comes back, or ours is removed, and MySQL restarts again, so the server
// is left running with what it had.
//
// The package's own mysqld.cnf sorts before this file, so ours is read later
// and wins. That read order is observed, not documented, so the live
// comparison is the real guarantee: a value overridden elsewhere fails the
// install and is rolled back.
func applyConfig(ctx context.Context, d sysexec.Deps, conf []byte, poolMiB int) (liveValues, error) {
	var live liveValues
	// A file that is already in place needs no restart when the server
	// already runs with its values.
	previous, err := os.ReadFile(d.Paths.MySQLConf())
	unchanged := err == nil && bytes.Equal(previous, conf)
	passes := 0
	restarted := false
	err = configtx.Apply(ctx, []configtx.Change{configtx.RootFile(d.Paths.MySQLConf(), conf, 0o644)}, func(ctx context.Context) error {
		// Apply runs the test again after a restore, and also when it
		// couldn't stage the file. Only the first pass with the candidate in
		// place checks the live values; a pass after that puts back what
		// the server had.
		current, rerr := os.ReadFile(d.Paths.MySQLConf())
		restore := passes > 0 || rerr != nil || !bytes.Equal(current, conf)
		passes++
		if restore {
			// A job is cancelled by stopping its unit, which ends ctx. The
			// restore must still run, or the server could be left with
			// settings that are not on disk.
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(context.WithoutCancel(ctx), queryTimeout+restartTimeout)
			defer cancel()
		}
		if err := validateConfig(ctx, d); err != nil {
			if restore {
				return fmt.Errorf("MySQL may be running settings that are not in %s: %w", d.Paths.MySQLConf(), err)
			}
			return err
		}
		if restore {
			if !restarted {
				return nil // the server never read the candidate
			}
			if err := restartMySQL(ctx, d); err != nil {
				return fmt.Errorf("MySQL may be running settings that are not in %s: %w", d.Paths.MySQLConf(), err)
			}
			return nil
		}
		if unchanged {
			if v, err := readLive(ctx, d); err == nil && v.check(poolMiB) == nil {
				live = v
				return nil
			}
		}
		restarted = true
		if err := restartMySQL(ctx, d); err != nil {
			return err
		}
		v, err := readLive(ctx, d)
		if err != nil {
			return err
		}
		live = v
		return v.check(poolMiB)
	})
	return live, err
}

// validateConfig runs mysqld --validate-config over the whole option chain,
// the new file included.
func validateConfig(ctx context.Context, d sysexec.Deps) error {
	res, err := d.Run.Run(ctx, sysexec.Cmd{Path: mysqld, Args: []string{"--validate-config"}})
	var exit *sysexec.ExitError
	if errors.As(err, &exit) {
		return &configtx.TestFailed{Output: strings.TrimSpace(string(res.Stdout) + string(res.Stderr))}
	}
	if err != nil {
		return err
	}
	if bytes.Contains(res.Stderr, []byte("[ERROR]")) {
		return &configtx.TestFailed{Output: strings.TrimSpace(string(res.Stderr))}
	}
	return nil
}

func restartMySQL(ctx context.Context, d sysexec.Deps) error {
	ctx, cancel := context.WithTimeout(ctx, restartTimeout)
	defer cancel()
	_, err := d.Run.Run(ctx, sysexec.Cmd{Path: systemctl, Args: []string{"restart", unit}})
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("restarting %s took longer than %s: %w", unit, restartTimeout, err)
	}
	return err
}

func readLive(ctx context.Context, d sysexec.Deps) (liveValues, error) {
	out, err := query(ctx, d, liveQuery)
	if err != nil {
		return liveValues{}, err
	}
	f := strings.Split(strings.TrimSuffix(out, "\n"), "\t")
	if len(f) != 7 {
		return liveValues{}, fmt.Errorf("MySQL answered %q to the query for its settings", out)
	}
	var pool uint64
	if _, err := fmt.Sscanf(f[4], "%d", &pool); err != nil {
		return liveValues{}, fmt.Errorf("MySQL's innodb_buffer_pool_size is %q", f[4])
	}
	return liveValues{bindAddress: f[0], localInfile: f[1], skipNameResolve: f[2], secureFilePriv: f[3], bufferPoolBytes: pool, mysqlx: f[5], version: f[6]}, nil
}

// check compares the live values with what the file asks for. MySQL rounds
// the buffer pool up to a multiple of its chunk size times its instances,
// at most 128 MiB times 8, so the live pool may be above the asked-for size
// by less than 1 GiB, and never below it.
func (v liveValues) check(poolMiB int) error {
	var wrong []string
	if v.bindAddress != "127.0.0.1" {
		wrong = append(wrong, fmt.Sprintf("bind_address is %q, want 127.0.0.1", v.bindAddress))
	}
	if v.localInfile != "0" {
		wrong = append(wrong, fmt.Sprintf("local_infile is %q, want 0", v.localInfile))
	}
	if v.skipNameResolve != "1" {
		wrong = append(wrong, fmt.Sprintf("skip_name_resolve is %q, want 1", v.skipNameResolve))
	}
	// MySQL reports the directory with a trailing slash.
	if strings.TrimSuffix(v.secureFilePriv, "/") != "/var/lib/mysql-files" {
		wrong = append(wrong, fmt.Sprintf("secure_file_priv is %q, want /var/lib/mysql-files", v.secureFilePriv))
	}
	if want := uint64(poolMiB) << 20; v.bufferPoolBytes < want || v.bufferPoolBytes-want >= 1<<30 {
		wrong = append(wrong, fmt.Sprintf("innodb_buffer_pool_size is %d, want %d", v.bufferPoolBytes, want))
	}
	if v.mysqlx != "DISABLED" && v.mysqlx != "" {
		wrong = append(wrong, fmt.Sprintf("the mysqlx plugin is %s, want DISABLED", v.mysqlx))
	}
	if len(wrong) > 0 {
		return fmt.Errorf("MySQL doesn't run with Zimam's settings: %s", strings.Join(wrong, "; "))
	}
	return nil
}

// ensureRootAuthSocket makes sure root@localhost logs in through the socket
// without a password. Ubuntu's package leaves it that way unless a root
// password was preseeded.
func ensureRootAuthSocket(ctx context.Context, d sysexec.Deps) error {
	out, err := query(ctx, d, rootPluginQuery)
	if err != nil {
		var exit *sysexec.ExitError
		if errors.As(err, &exit) && (bytes.Contains(exit.Stderr, []byte("ERROR 1045")) || bytes.Contains(exit.Stderr, []byte("Access denied"))) {
			return &contract.AgentError{Code: contract.ErrConflict, Message: "MySQL's root@localhost doesn't accept the socket login, because it has a password. " +
				"Zimam needs root@localhost on auth_socket, so it changed nothing. Run ALTER USER 'root'@'localhost' IDENTIFIED WITH auth_socket as that root, then install again."}
		}
		return fmt.Errorf("checking MySQL's root login: %w", err)
	}
	plugin := strings.TrimSpace(out)
	if plugin == "" {
		return errors.New("MySQL has no account root@localhost")
	}
	if plugin == "auth_socket" {
		return nil
	}
	d.Logf("root@localhost uses %s; switching it to auth_socket", plugin)
	_, err = query(ctx, d, rootAuthSocket)
	return err
}
