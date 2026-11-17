// Package valkey installs Valkey from Ubuntu's archive and talks to it as its
// admin user over the local network (v0.3.0). Every command is built here
// from constants and validated values.
package valkey

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"zimam/agent/internal/configtx"
	"zimam/agent/internal/metrics"
	"zimam/agent/internal/paths"
	"zimam/agent/internal/php"
	"zimam/agent/internal/services"
	"zimam/agent/internal/sysexec"
	"zimam/agent/internal/templates"
)

const (
	packageName  = "valkey-server"
	unit         = "valkey-server"
	valkeyServer = "/usr/bin/valkey-server"
	runuser      = "/usr/sbin/runuser"
	systemctl    = "/usr/bin/systemctl"
	serviceUser  = "valkey" // the package's user and group

	// aclPath is where the live server reads its users from, as the rendered
	// config says.
	aclPath = "/etc/zimam/valkey/users.acl"
)

// The time limits of the steps after the packages. The job may run 600 s.
// apt-get may wait up to 300 s for a dpkg lock, then downloads and installs.
// The worst case of everything else:
//
//	waiting for the ACL lock          lockWait               60 s
//	candidate server, to stop it      startTimeout + cliTimeout + stopWait
//	                                                      15 + 10 + 10 s
//	systemd reload and the restart    reloadTimeout + restartTimeout
//	                                                      10 + 60 s
//	live checks (PING, CONFIG GET,
//	ACL LIST, INFO)                   4 * cliTimeout       40 s
//	restore after a failure           2 * reloadTimeout + restartTimeout
//	                                  (reload, reset-failed, restart)
//	                                                      10 + 10 + 60 s
//
// which is 285 s and leaves 315 s for apt-get. The package's unit has
// TimeoutStopSec=0, which never gives up, so a restart is bounded here and
// only here; a timed-out systemctl restart leaves its job queued in systemd,
// which finishes or fails on its own.
var (
	lockWait       = time.Minute
	startTimeout   = 15 * time.Second
	stopWait       = 10 * time.Second
	reloadTimeout  = 10 * time.Second
	restartTimeout = time.Minute
)

// notApplied is logged when the packages are in but Zimam's settings are not.
const notApplied = "Valkey is installed with the package's own settings and is not recorded as installed yet; running the install again continues from here."

// Install installs Valkey from Ubuntu's archive, writes and tests Zimam's
// config, ACL file and systemd drop-in, and records the install in
// services.json. It returns the version running. Running it again
// converges: the files are rendered again, the admin password stays, and a
// server that already runs them is not restarted.
//
// users lists the sites' Valkey users, which the ACL file must hold. It is
// called under the ACL lock, so the file is never rendered from a list that
// another change has since outdated.
func Install(ctx context.Context, d sysexec.Deps, users func() ([]SiteUser, error)) (string, error) {
	mem, err := metrics.MemTotal(d.Paths)
	if err != nil {
		return "", err
	}
	d.Logf("Installing Valkey")
	if err := php.InstallPackages(ctx, d, packageName); err != nil {
		return "", err
	}
	uid, gid, err := serviceIDs(d.Paths)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(d.Paths.LocksDir(), 0o700); err != nil {
		return "", err
	}
	lockCtx, cancel := context.WithTimeout(ctx, lockWait)
	unlock, err := sysexec.Lock(lockCtx, d.Paths.ValkeyACLLock())
	cancel()
	if err != nil {
		return "", fmt.Errorf("waiting for the lock on Valkey's users: %w", err)
	}
	defer unlock()

	siteUsers, err := users()
	if err != nil {
		return "", err
	}
	if err := ensureDir(d.Paths.ValkeyDir(), 0o750, gid); err != nil {
		return "", err
	}
	if err := ensureDir(d.Paths.ValkeyDropInDir(), 0o755, -1); err != nil {
		return "", err
	}
	pass, err := ensureAdminPassword(d)
	if err != nil {
		return "", err
	}
	memMiB := MaxMemoryMiB(mem)
	acl, err := RenderACL(sha256.Sum256([]byte(pass)), siteUsers)
	if err != nil {
		return "", err
	}
	conf, err := templates.Render("valkey/valkey.conf", templates.ValkeyConf{MaxMemoryMiB: memMiB})
	if err != nil {
		return "", err
	}
	dropIn, err := templates.Render("valkey/valkey-server-dropin.conf", nil)
	if err != nil {
		return "", err
	}
	d.Logf("Writing Valkey's settings (a %d MiB memory limit)", memMiB)
	live, err := applyFiles(ctx, d, files{conf: conf, acl: acl, dropIn: dropIn}, uid, gid, memMiB)
	if err != nil {
		d.Logf("%s", notApplied)
		return "", err
	}
	d.Logf("Valkey %s is running", live.version)
	if err := services.Update(d.Paths, func(s *services.State) { s.Valkey = services.Entry{Installed: true, Version: live.version} }); err != nil {
		return "", err
	}
	return live.version, nil
}

// MaxMemoryMiB is the memory Valkey may use for data: a tenth of the
// server's, in whole MiB, and at least 16. A zero would mean no limit.
func MaxMemoryMiB(memTotalBytes uint64) int {
	return int(max(memTotalBytes/10>>20, 16))
}

// serviceIDs are the package's valkey user and group ids.
func serviceIDs(p paths.Paths) (uid, gid int, err error) {
	if uid, err = idOf(p.EtcPasswd(), serviceUser); err != nil {
		return 0, 0, err
	}
	if gid, err = idOf(p.EtcGroup(), serviceUser); err != nil {
		return 0, 0, err
	}
	return uid, gid, nil
}

// idOf is the id in the third field of name's line in a passwd or group file.
func idOf(path, name string) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	for line := range strings.Lines(string(data)) {
		f := strings.Split(strings.TrimSuffix(line, "\n"), ":")
		if len(f) >= 3 && f[0] == name {
			id, err := strconv.Atoi(f[2])
			if err != nil {
				return 0, fmt.Errorf("%s: %s has the id %q", path, name, f[2])
			}
			return id, nil
		}
	}
	return 0, fmt.Errorf("%s has no %s; the valkey-server package makes it", path, name)
}

// ensureDir makes dir with this mode, owned by group gid unless that is -1.
func ensureDir(dir string, mode os.FileMode, gid int) error {
	if fi, err := os.Lstat(dir); err == nil && !fi.IsDir() {
		return fmt.Errorf("%s is not a directory", dir)
	}
	// A parent that is missing is made world-readable, never with this mode:
	// the valkey user has to reach the directory through it.
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return err
	}
	if err := os.Mkdir(dir, mode); err != nil && !errors.Is(err, fs.ErrExist) {
		return err
	}
	if err := os.Chown(dir, -1, gid); err != nil {
		return err
	}
	return os.Chmod(dir, mode)
}

// ensureAdminPassword returns the admin password in admin.pass (root 0600),
// making it once from 32 random bytes if there is none. A password that is
// already there stays, so a re-run doesn't change what a running server
// accepts.
func ensureAdminPassword(d sysexec.Deps) (string, error) {
	pass, found, err := readAdminPassword(d)
	if err != nil {
		return "", err
	}
	path := d.Paths.ValkeyAdminPass()
	if found {
		return pass, os.Chmod(path, 0o600)
	}
	if _, err := os.Lstat(path); err == nil {
		d.Logf("%s isn't a password Zimam made; making a new one", path)
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	pass = base64.RawURLEncoding.EncodeToString(raw)
	if err := writeAtomically(path, []byte(pass+"\n")); err != nil {
		return "", err
	}
	d.Logf("Made Valkey's admin password in %s", path)
	return pass, nil
}

// writeAtomically writes a root 0600 file that a reader sees whole or not at
// all.
func writeAtomically(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".admin-pass-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // fails harmlessly once renamed
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// files are the three files Install writes, rendered.
type files struct{ conf, acl, dropIn []byte }

// applyFiles writes the files and keeps them only if the server runs with
// them. The test, in order: a candidate server started from the files (the
// real server isn't touched), systemd told of a new drop-in, the real
// server's restart within a time limit, and the live values read as the
// admin: they must be what the files ask for. If any step fails the previous
// files come back, or ours are removed, and the real server restarts if it
// had read the candidate, so it is left running what it had.
func applyFiles(ctx context.Context, d sysexec.Deps, f files, uid, gid, memMiB int) (liveValues, error) {
	p := d.Paths
	var live liveValues
	same := func(path string, content []byte) bool {
		b, err := os.ReadFile(path)
		return err == nil && bytes.Equal(b, content)
	}
	dropInSame := same(p.ValkeyDropIn(), f.dropIn)
	// Files already in place need no restart when the server already runs
	// with them.
	unchanged := dropInSame && same(p.ValkeyConf(), f.conf) && same(p.ValkeyACL(), f.acl)

	passes := 0
	var reloaded, restarted bool
	changes := []configtx.Change{
		{Path: p.ValkeyConf(), Content: f.conf, Mode: 0o640, UID: -1, GID: gid},
		{Path: p.ValkeyACL(), Content: f.acl, Mode: 0o640, UID: -1, GID: gid},
		configtx.RootFile(p.ValkeyDropIn(), f.dropIn, 0o644),
	}
	err := configtx.Apply(ctx, changes, func(ctx context.Context) error {
		// Apply runs the test again after a restore, and also when it
		// couldn't stage the files. Only the first pass with the candidate in
		// place checks anything; a pass after that puts back what the server
		// had.
		restore := passes > 0 || !same(p.ValkeyConf(), f.conf) || !same(p.ValkeyACL(), f.acl) || !same(p.ValkeyDropIn(), f.dropIn)
		passes++
		if restore {
			// A job is cancelled by stopping its unit, which ends ctx. The
			// restore must still run, or the server could be left with
			// settings that are not on disk.
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(context.WithoutCancel(ctx), 2*reloadTimeout+restartTimeout)
			defer cancel()
			if reloaded && !dropInSame {
				if err := systemctlRun(ctx, d, reloadTimeout, "daemon-reload"); err != nil {
					return fmt.Errorf("Valkey may be running settings that are not in %s: %w", p.ValkeyDir(), err)
				}
			}
			if restarted {
				// The failed start and this one may be more than systemd allows in
				// a few seconds (StartLimitBurst), and the server would stay down.
				// Clearing the count is no error if there is nothing to clear.
				if err := systemctlRun(ctx, d, reloadTimeout, "reset-failed", unit); err != nil {
					d.Logf("systemctl reset-failed %s: %v", unit, err)
				}
				if err := systemctlRun(ctx, d, restartTimeout, "restart", unit); err != nil {
					return fmt.Errorf("Valkey may be running settings that are not in %s: %w", p.ValkeyDir(), err)
				}
			}
			return nil
		}
		if unchanged {
			if v, err := readLive(ctx, d); err == nil && v.check(memMiB) == nil {
				live = v
				return nil
			}
		}
		if err := testCandidate(ctx, d, uid, gid); err != nil {
			return err
		}
		restarted = true
		if !dropInSame {
			reloaded = true
			if err := systemctlRun(ctx, d, reloadTimeout, "daemon-reload"); err != nil {
				return err
			}
		}
		if err := systemctlRun(ctx, d, restartTimeout, "restart", unit); err != nil {
			return err
		}
		v, err := readLive(ctx, d)
		if err != nil {
			return err
		}
		live = v
		return v.check(memMiB)
	})
	return live, err
}

// systemctlRun runs systemctl with a time limit, which a hung unit's start
// or stop would otherwise exhaust only at the job's end.
func systemctlRun(ctx context.Context, d sysexec.Deps, limit time.Duration, args ...string) error {
	ctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	_, err := d.Run.Run(ctx, sysexec.Cmd{Path: systemctl, Args: args})
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("systemctl %s took longer than %s: %w", strings.Join(args, " "), limit, err)
	}
	return err
}

// candidateOutcome is how the candidate server's process ended.
type candidateOutcome struct {
	res sysexec.Result
	err error
}

// testCandidate starts a server from the files in place, as user valkey on a
// unix socket only (it has no port), in a directory of its own, and asks it
// PING as the admin. valkey-server has no option to only check a config: a
// bad file makes it exit at once, and this is how that is seen. The server
// is stopped and its directory removed on every way out, a cancel included.
// It runs outside the unit's sandbox, so it can't catch a failure the
// sandbox causes; the restart that follows does.
func testCandidate(ctx context.Context, d sysexec.Deps, uid, gid int) error {
	p := d.Paths
	dir := p.ValkeyTestDir()
	if err := os.RemoveAll(dir); err != nil { // one left by a job that was killed
		return err
	}
	if err := os.Mkdir(dir, 0o700); err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	if err := os.Chown(dir, uid, gid); err != nil {
		return err
	}
	socket := filepath.Join(dir, "s.sock")

	runCtx, stop := context.WithCancel(ctx)
	done := make(chan candidateOutcome, 1)
	exited := false
	defer func() {
		stop() // kills the whole process group
		if !exited {
			select {
			case <-done:
			case <-time.After(stopWait):
			}
		}
	}()
	go func() {
		res, err := d.Run.Run(runCtx, sysexec.Cmd{Path: runuser, KillGroup: true, Args: []string{
			"-u", serviceUser, "--", valkeyServer, p.ValkeyConf(),
			"--aclfile", p.ValkeyACL(),
			"--port", "0", "--unixsocket", socket,
			"--daemonize", "no", "--supervised", "no",
			"--pidfile", filepath.Join(dir, "p"),
			"--logfile", "", "--dir", dir,
			"--save", "", "--appendonly", "no",
		}})
		done <- candidateOutcome{res, err}
	}()

	deadline := time.NewTimer(startTimeout)
	defer deadline.Stop()
	var last error
	for {
		select {
		case o := <-done:
			exited = true
			if ctx.Err() != nil {
				return ctx.Err()
			}
			out := tail(strings.TrimSpace(string(o.res.Stdout) + string(o.res.Stderr)))
			if o.err != nil && out == "" {
				out = o.err.Error()
			}
			return &configtx.TestFailed{Output: "a server started from the files stopped before it answered:\n" + out}
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			msg := fmt.Sprintf("a server started from the files didn't answer within %s", startTimeout)
			if last != nil {
				msg += ": " + last.Error()
			}
			return &configtx.TestFailed{Output: msg}
		case <-time.After(50 * time.Millisecond):
		}
		if _, err := os.Stat(socket); err != nil {
			continue
		}
		out, err := cliSocket(ctx, d, socket, "PING")
		if err == nil && out == "PONG" {
			return nil
		}
		if err == nil {
			err = fmt.Errorf("PING was answered %q", out)
		}
		last = err
	}
}

// liveValues are what the running server reports.
type liveValues struct {
	bind, port, aclFile, maxMemory, policy string
	defaultOff                             bool
	version                                string
}

// readLive asks the running server, as the admin, for what it runs with.
// That the admin logs in at all is part of it.
func readLive(ctx context.Context, d sysexec.Deps) (liveValues, error) {
	var v liveValues
	out, err := Cli(ctx, d, "PING")
	if err != nil {
		return v, err
	}
	if out != "PONG" {
		return v, fmt.Errorf("Valkey answered PING with %q", out)
	}
	if out, err = Cli(ctx, d, "CONFIG", "GET", "bind", "port", "aclfile", "maxmemory", "maxmemory-policy"); err != nil {
		return v, err
	}
	// Each setting prints as its name and then its value, one per line.
	lines := strings.Split(out, "\n")
	if len(lines)%2 != 0 {
		return v, fmt.Errorf("Valkey answered %q to the query for its settings", out)
	}
	cfg := map[string]string{}
	for i := 0; i < len(lines); i += 2 {
		cfg[lines[i]] = strings.TrimRight(lines[i+1], "\r")
	}
	v.bind, v.port, v.aclFile, v.maxMemory, v.policy = cfg["bind"], cfg["port"], cfg["aclfile"], cfg["maxmemory"], cfg["maxmemory-policy"]

	if out, err = Cli(ctx, d, "ACL", "LIST"); err != nil {
		return v, err
	}
	seen := false
	for line := range strings.Lines(out) {
		if f := strings.Fields(line); len(f) >= 3 && f[0] == "user" && f[1] == "default" {
			seen, v.defaultOff = true, f[2] == "off"
		}
	}
	if !seen {
		return v, errors.New("Valkey's ACL LIST has no default user")
	}

	if out, err = Cli(ctx, d, "INFO", "server"); err != nil {
		return v, err
	}
	for line := range strings.Lines(out) {
		if rest, ok := strings.CutPrefix(strings.TrimRight(line, "\r\n"), "valkey_version:"); ok {
			v.version = rest
		}
	}
	return v, nil
}

// check compares the live values with what the files ask for. The live
// server must not listen beyond loopback, must read Zimam's ACL file, must
// have the memory limit and the no-eviction policy, and must have the
// default user off, whatever a file read later or a leftover setting says.
func (v liveValues) check(memMiB int) error {
	var wrong []string
	if v.bind != "127.0.0.1 -::1" {
		wrong = append(wrong, fmt.Sprintf("bind is %q, want 127.0.0.1 -::1", v.bind))
	}
	if v.port != "6379" {
		wrong = append(wrong, fmt.Sprintf("port is %q, want 6379", v.port))
	}
	if v.aclFile != aclPath {
		wrong = append(wrong, fmt.Sprintf("aclfile is %q, want %s", v.aclFile, aclPath))
	}
	if want := strconv.FormatUint(uint64(memMiB)<<20, 10); v.maxMemory != want {
		wrong = append(wrong, fmt.Sprintf("maxmemory is %q, want %s", v.maxMemory, want))
	}
	if v.policy != "noeviction" {
		wrong = append(wrong, fmt.Sprintf("maxmemory-policy is %q, want noeviction", v.policy))
	}
	if !v.defaultOff {
		wrong = append(wrong, "the default user is not off")
	}
	if v.version == "" {
		wrong = append(wrong, "its version is not reported")
	}
	if len(wrong) > 0 {
		return fmt.Errorf("Valkey doesn't run with Zimam's settings: %s", strings.Join(wrong, "; "))
	}
	return nil
}

// tail is the end of a server's output, where the reason it stopped is.
func tail(s string) string {
	const n = 800
	if len(s) > n {
		return "..." + s[len(s)-n:]
	}
	return s
}
