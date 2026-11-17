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
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"zimam/agent/contract"
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

// The time limits of the steps around the packages. The job may run 900 s.
//
// Before apt-get, when a Zimam file is missing, the service is masked,
// stopped and checked: 10 + 60 + 10 s. apt-get may wait up to 300 s
// for a dpkg lock, then downloads and installs. After it, the worst case of
// everything else is what needed() adds up:
//
//	waiting for the ACL lock             lockWait              60 s
//	the skip check of a re-run           reloadTimeout + 4 * cliTimeout
//	                                     (is-enabled, then the live checks)
//	                                                           10 + 40 s
//	candidate server, its checks over
//	the socket, and stopping it          startTimeout + 3 * cliTimeout + stopWait
//	                                                           15 + 30 + 10 s
//	unmask, reload, enable               3 * reloadTimeout     30 s
//	the restart                          restartTimeout        60 s
//	live checks (PING, CONFIG GET,
//	ACL LIST, INFO)                      4 * cliTimeout        40 s
//	undoing a failure                    2 * reloadTimeout + restartTimeout
//	                                     (reload, then reset-failed and restart,
//	                                     or stop and mask)     10 + 10 + 60 s
//
// which is 375 s with the lock wait. With the 80 s before apt-get and its own
// lock wait of 300 s that is 755 s, so apt-get has about 145 s of the 900 for
// its download and install.
//
// Install refuses to start the files' test when the job's deadline leaves
// less than needed() after the lock, so a slow apt-get can't leave the undo to
// be cut off. The package's unit has TimeoutStopSec=0, which never gives up,
// so a restart or stop is bounded here and only here; a timed-out systemctl
// leaves its job queued in systemd, which finishes or fails on its own.
var (
	lockWait       = time.Minute
	startTimeout   = 15 * time.Second
	stopWait       = 10 * time.Second
	reloadTimeout  = 10 * time.Second
	restartTimeout = time.Minute
)

// needed is the time the files' test, the restart and the undo of a failure
// may take after the lock is held: the worst case in the table above.
func needed() time.Duration {
	live := 4 * cliTimeout
	candidate := startTimeout + 3*cliTimeout + stopWait
	forward := reloadTimeout + live + candidate + 3*reloadTimeout + restartTimeout + live
	undo := 2*reloadTimeout + restartTimeout
	return forward + undo
}

// Install installs Valkey from Ubuntu's archive, writes and tests Zimam's
// config, ACL file and systemd drop-in, and records the install in
// services.json. It returns the version running. Running it again
// converges: the files are rendered again, the admin password stays, and a
// server that already runs them is not restarted.
//
// The package's server must never run with the package's own settings, which
// let anyone on the machine in without a password. So while Zimam's files are
// not all in place, the service is stopped and masked before apt-get, and
// unmasked and started only after the files passed their test. If they fail,
// it stays stopped and masked. A successful run always ends with the service
// unmasked, enabled and running on the files, whatever state an earlier run
// left it in.
//
// users lists the sites' Valkey users, which the ACL file must hold. It is
// called under the ACL lock, so the file is never rendered from a list that
// another change has since outdated.
func Install(ctx context.Context, d sysexec.Deps, users func() ([]SiteUser, error)) (string, error) {
	mem, err := metrics.MemTotal(d.Paths)
	if err != nil {
		return "", err
	}
	recorded, err := services.Load(d.Paths)
	if err != nil {
		return "", err
	}
	// While a Zimam file is missing there is no complete configuration of
	// ours to run, recorded or not: the service must not run, or be restarted,
	// on the package's settings.
	held := !filesInPlace(d.Paths)
	d.Logf("Installing Valkey")
	if held {
		if recorded.Valkey.Installed {
			d.Logf("A file of Zimam's Valkey settings is missing, so Valkey is stopped until the files are made again and tested; sites have no Valkey meanwhile.")
		}
		if err := holdUnit(ctx, d); err != nil {
			return "", err
		}
	}
	if err := php.InstallPackages(ctx, d, packageName); err != nil {
		if held {
			d.Logf("%s", maskedNote)
		}
		return "", err
	}
	fail := func(err error) (string, error) {
		d.Logf("%s", failureNote(held, recorded.Valkey.Installed))
		return "", err
	}
	if held && !recorded.Valkey.Installed {
		// Data from before Zimam managed Valkey could hold keys planted by
		// anyone, under any site's prefix.
		if err := refuseExistingData(d.Paths); err != nil {
			return fail(err)
		}
	}
	uid, gid, err := serviceIDs(d.Paths)
	if err != nil {
		return fail(err)
	}
	if err := os.MkdirAll(d.Paths.LocksDir(), 0o700); err != nil {
		return fail(err)
	}
	lockCtx, cancel := context.WithTimeout(ctx, lockWait)
	unlock, err := sysexec.Lock(lockCtx, d.Paths.ValkeyACLLock())
	cancel()
	if err != nil {
		return fail(fmt.Errorf("waiting for the lock on Valkey's users: %w", err))
	}
	defer unlock()

	// The job's time limit is a systemd one that this context doesn't carry,
	// but a deadline it does carry must leave room for the undo.
	if dl, ok := ctx.Deadline(); ok {
		if left := dl.Sub(d.Now()); left < needed() {
			return fail(fmt.Errorf("only %s of this job's time is left, and testing Valkey's files, restarting it and undoing a failure can take %s; run the install again", left.Round(time.Second), needed()))
		}
	}
	siteUsers, err := users()
	if err != nil {
		return fail(err)
	}
	if err := ensureDir(d.Paths.ValkeyDir(), 0o750, gid); err != nil {
		return fail(err)
	}
	if err := ensureDir(d.Paths.ValkeyDropInDir(), 0o755, -1); err != nil {
		return fail(err)
	}
	pass, newPass, err := adminPassword(d)
	if err != nil {
		return fail(err)
	}
	memMiB := MaxMemoryMiB(mem)
	acl, err := RenderACL(sha256.Sum256([]byte(pass)), siteUsers)
	if err != nil {
		return fail(err)
	}
	conf, err := templates.Render("valkey/valkey.conf", templates.ValkeyConf{MaxMemoryMiB: memMiB})
	if err != nil {
		return fail(err)
	}
	dropIn, err := templates.Render("valkey/valkey-server-dropin.conf", nil)
	if err != nil {
		return fail(err)
	}
	f := files{conf: conf, acl: acl, dropIn: dropIn, adminPass: newPass}
	d.Logf("Writing Valkey's settings (a %d MiB memory limit)", memMiB)
	live, err := applyFiles(ctx, d, f, target{uid: uid, gid: gid, memMiB: memMiB, held: held, recorded: recorded.Valkey.Installed})
	if err != nil {
		return fail(err)
	}
	d.Logf("Valkey %s is running", live.version)
	if err := services.Update(d.Paths, func(s *services.State) { s.Valkey = services.Entry{Installed: true, Version: live.version} }); err != nil {
		return "", err
	}
	return live.version, nil
}

// maskedNote and failureNote go in the job's log when the install stops.
const maskedNote = "Valkey's service is stopped and masked, so no server runs with the package's own settings; running the install again continues from here."

func failureNote(held, recorded bool) string {
	switch {
	case held && recorded:
		return maskedNote + " Sites have no Valkey until it does."
	case held:
		return maskedNote
	case recorded:
		return "Valkey keeps running the settings it had before this install; nothing recorded has changed."
	default:
		return "Valkey's files are in place but the install is not recorded; running the install again continues from here."
	}
}

// MaxMemoryMiB is the memory Valkey may use for data: a tenth of the
// server's, in whole MiB, and at least 16. A zero would mean no limit.
func MaxMemoryMiB(memTotalBytes uint64) int {
	return int(max(memTotalBytes/10>>20, 16))
}

// filesInPlace reports whether all three of Zimam's files exist: the server
// then already runs on them, or did, and the package's own settings are no
// longer in play.
func filesInPlace(p paths.Paths) bool {
	for _, path := range []string{p.ValkeyConf(), p.ValkeyACL(), p.ValkeyDropIn()} {
		if _, err := os.Lstat(path); err != nil {
			return false
		}
	}
	return true
}

// holdUnit masks the service, stops it and checks that it is stopped, so that
// the package's postinst can't start a server on the package's settings, and
// one that an administrator already started doesn't keep running them.
func holdUnit(ctx context.Context, d sysexec.Deps) error {
	if err := systemctlRun(ctx, d, reloadTimeout, "mask", unit); err != nil {
		return err
	}
	var exit *sysexec.ExitError
	// A unit that isn't installed yet can't be stopped, which is fine.
	if err := systemctlRun(ctx, d, restartTimeout, "stop", unit); err != nil && !errors.As(err, &exit) {
		return err
	}
	err := systemctlRun(ctx, d, reloadTimeout, "is-active", "--quiet", unit)
	switch {
	case err == nil:
		return fmt.Errorf("%s is still running after it was stopped and masked", unit)
	case errors.As(err, &exit):
		return nil
	default:
		return err
	}
}

// refuseExistingData refuses to manage a Valkey whose data directory holds
// data when Zimam has no record of installing Valkey. Zimam's server would load
// it, and the keys in it could lie in any site's prefix. On a recorded server
// the data is the sites' own and this is not asked. Nothing is deleted.
func refuseExistingData(p paths.Paths) error {
	dir := p.ValkeyDataDir()
	var found []string
	if _, err := os.Lstat(filepath.Join(dir, "dump.rdb")); err == nil {
		found = append(found, "dump.rdb")
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if entries, err := os.ReadDir(filepath.Join(dir, "appendonlydir")); err == nil && len(entries) > 0 {
		found = append(found, "appendonlydir")
	} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if len(found) == 0 {
		return nil
	}
	return &contract.AgentError{Code: contract.ErrConflict, Message: fmt.Sprintf(
		"%s holds data that Zimam did not record installing Valkey with: %s. It may be from before Zimam managed Valkey, "+
			"or left by an install that was interrupted. Check it, then remove or move the %s from that directory and run the install again. "+
			"Zimam deleted nothing; the Valkey service is stopped and masked.", dir, strings.Join(found, ", "), strings.Join(found, " and "))}
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

// adminPassword returns the admin password. One already in admin.pass stays,
// so a re-run doesn't change what a running server accepts, and fresh is
// nil. Otherwise a new one is made from 32 random bytes and returned as
// fresh, to be written with the other files in the same transaction: written
// alone, a failed test would restore an ACL whose hash it no longer matches.
func adminPassword(d sysexec.Deps) (pass string, fresh []byte, err error) {
	pass, found, err := readAdminPassword(d)
	if err != nil {
		return "", nil, err
	}
	path := d.Paths.ValkeyAdminPass()
	if found {
		return pass, nil, os.Chmod(path, 0o600)
	}
	if _, err := os.Lstat(path); err == nil {
		d.Logf("%s isn't a password Zimam made; making a new one", path)
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", nil, err
	}
	pass = base64.RawURLEncoding.EncodeToString(raw)
	d.Logf("Making Valkey's admin password in %s", path)
	return pass, []byte(pass + "\n"), nil
}

// files are what Install writes, rendered. adminPass is nil when admin.pass
// already holds the password.
type files struct{ conf, acl, dropIn, adminPass []byte }

// target is what the files are written for.
type target struct {
	uid, gid, memMiB int
	// held is set when a Zimam file was missing: the service is masked and
	// stopped, and stays so unless the files pass their test. There is then no
	// complete previous configuration of ours to go back to.
	held bool
	// recorded is set when services.json says Zimam installed Valkey, so what
	// is in its data directory is the sites' own and is never deleted.
	recorded bool
}

// aclUsers are the user names an ACL file defines.
func aclUsers(acl []byte) []string {
	var names []string
	for line := range strings.Lines(string(acl)) {
		if f := strings.Fields(line); len(f) >= 2 && f[0] == "user" {
			names = append(names, f[1])
		}
	}
	slices.Sort(names)
	return names
}

// applyFiles writes the files and keeps them only if the server runs with
// them. The test, in order: a candidate server started from the files (the
// real server isn't touched), then, for a first install, the service unmasked
// and enabled, systemd told of a new drop-in, the real server's start within a
// time limit, and the live values read as the admin: they must be what the
// files ask for. If any step fails the previous files come back, or ours are
// removed. A server that had read the candidate then restarts on the previous
// files; one that is a first install is stopped and masked again, since it
// has no previous settings of ours to run.
func applyFiles(ctx context.Context, d sysexec.Deps, f files, t target) (liveValues, error) {
	p := d.Paths
	var live liveValues
	same := func(path string, content []byte) bool {
		b, err := os.ReadFile(path)
		return err == nil && bytes.Equal(b, content)
	}
	dropInSame := same(p.ValkeyDropIn(), f.dropIn)
	// Files already in place need no restart when the server already runs
	// with them.
	unchanged := f.adminPass == nil && dropInSame && same(p.ValkeyConf(), f.conf) && same(p.ValkeyACL(), f.acl)
	users := aclUsers(f.acl)

	passes := 0
	var unmasked, reloaded, restarted bool
	changes := []configtx.Change{
		{Path: p.ValkeyConf(), Content: f.conf, Mode: 0o640, UID: -1, GID: t.gid},
		{Path: p.ValkeyACL(), Content: f.acl, Mode: 0o640, UID: -1, GID: t.gid},
		configtx.RootFile(p.ValkeyDropIn(), f.dropIn, 0o644),
	}
	if f.adminPass != nil {
		changes = append(changes, configtx.RootFile(p.ValkeyAdminPass(), f.adminPass, 0o600))
	}
	err := configtx.Apply(ctx, changes, func(ctx context.Context) error {
		// Apply runs the test again after a restore, and also when it
		// couldn't stage the files. Only the first pass with the candidate in
		// place checks anything; a pass after that puts back what the server
		// had.
		// The new admin password counts too: if staging it failed after the
		// others were already as wanted, this call is the undo.
		restore := passes > 0 || !same(p.ValkeyConf(), f.conf) || !same(p.ValkeyACL(), f.acl) || !same(p.ValkeyDropIn(), f.dropIn) ||
			(f.adminPass != nil && !same(p.ValkeyAdminPass(), f.adminPass))
		passes++
		if restore {
			// A job is cancelled by stopping its unit, which ends ctx. The
			// restore must still run, or the server could be left with
			// settings that are not on disk.
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(context.WithoutCancel(ctx), 2*reloadTimeout+restartTimeout)
			defer cancel()
			return undo(ctx, d, t, dropInSame, unmasked, reloaded, restarted)
		}
		// A server that is usable is enabled and answers as configured. One that
		// a killed or failed run left masked or disabled is brought up below.
		if unchanged && enabled(ctx, d) {
			if v, err := readLive(ctx, d); err == nil && v.check(t.memMiB, users) == nil {
				live = v
				return nil
			}
		}
		if err := testCandidate(ctx, d, t.uid, t.gid, users); err != nil {
			return err
		}
		// Whatever state an earlier run left the unit in, a successful run ends
		// with it unmasked, enabled and started on these files.
		unmasked = true
		if err := systemctlRun(ctx, d, reloadTimeout, "unmask", unit); err != nil {
			return err
		}
		if t.held || !dropInSame {
			reloaded = true
			if err := systemctlRun(ctx, d, reloadTimeout, "daemon-reload"); err != nil {
				return err
			}
		}
		if err := systemctlRun(ctx, d, reloadTimeout, "enable", unit); err != nil {
			return err
		}
		restarted = true
		if err := systemctlRun(ctx, d, restartTimeout, "restart", unit); err != nil {
			return err
		}
		v, err := readLive(ctx, d)
		if err != nil {
			return err
		}
		live = v
		return v.check(t.memMiB, users)
	})
	return live, err
}

// undo puts the service back as it was before the test touched it. Its errors
// say that the server may run settings that are not on disk.
func undo(ctx context.Context, d sysexec.Deps, t target, dropInSame, unmasked, reloaded, restarted bool) error {
	p := d.Paths
	may := func(err error) error {
		return fmt.Errorf("Valkey may be running settings that are not in %s: %w", p.ValkeyDir(), err)
	}
	if reloaded && !dropInSame {
		if err := systemctlRun(ctx, d, reloadTimeout, "daemon-reload"); err != nil {
			return may(err)
		}
	}
	if t.held {
		// There are no previous settings of ours to run, and the package's must
		// not run: stop the service and mask it again.
		if unmasked {
			if err := systemctlRun(ctx, d, restartTimeout, "stop", unit); err != nil {
				return may(err)
			}
			if err := systemctlRun(ctx, d, reloadTimeout, "mask", unit); err != nil {
				return may(err)
			}
		}
		if restarted && !t.recorded {
			// The server saves a dump when it stops. On a server Zimam never
			// recorded, nothing was in the data directory before this first start
			// (it was checked), and only Zimam's server with no users but the admin
			// ran, so the dump is Zimam's and is removed: left, the next install
			// would refuse it. On a recorded server the data is the sites' own, and is
			// never deleted.
			if err := os.Remove(filepath.Join(p.ValkeyDataDir(), "dump.rdb")); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return may(err)
			}
		}
		return nil
	}
	if restarted {
		// The failed start and this one may be more than systemd allows in a
		// few seconds (StartLimitBurst), and the server would stay down.
		// Clearing the count is no error if there is nothing to clear.
		if err := systemctlRun(ctx, d, reloadTimeout, "reset-failed", unit); err != nil {
			d.Logf("systemctl reset-failed %s: %v", unit, err)
		}
		if err := systemctlRun(ctx, d, restartTimeout, "restart", unit); err != nil {
			return may(err)
		}
	}
	return nil
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
func testCandidate(ctx context.Context, d sysexec.Deps, uid, gid int, users []string) error {
	p := d.Paths
	dir := p.ValkeyTestDir()
	killStale(d, dir, uid) // one left by a job that was killed
	if err := os.RemoveAll(dir); err != nil {
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
			return checkCandidate(ctx, d, socket, users)
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
	users                                  []string // sorted
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
		f := strings.Fields(line)
		if len(f) >= 2 && f[0] == "user" {
			v.users = append(v.users, f[1])
		}
		if len(f) >= 3 && f[0] == "user" && f[1] == "default" {
			seen, v.defaultOff = true, f[2] == "off"
		}
	}
	if !seen {
		return v, errors.New("Valkey's ACL LIST has no default user")
	}
	slices.Sort(v.users)

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
// default user off, and must have the users the ACL file defines, whatever a
// file read later, a leftover setting or a change made at run time says.
func (v liveValues) check(memMiB int, users []string) error {
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
	if !slices.Equal(v.users, users) {
		wrong = append(wrong, fmt.Sprintf("its users are %v, want %v", v.users, users))
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

// killProcess ends a process; tests replace it.
var killProcess = syscall.Kill

// killStale ends a candidate server that a killed job left running: its
// directory is about to be removed, and the process would run on without it.
// The pid comes from the pidfile it wrote, and the process is signalled only if
// /proc shows it is a valkey-server that user uid owns, since the number may
// have been given to another process since. The name is the process name,
// since valkey-server is a link to valkey-check-rdb and /proc/<pid>/exe names
// the latter. The owner comes from the status
// file: the /proc directory of a process that changed its user belongs to root.
func killStale(d sysexec.Deps, dir string, uid int) {
	b, err := os.ReadFile(filepath.Join(dir, "p"))
	if err != nil {
		return
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || pid <= 1 {
		return
	}
	proc := d.Paths.ProcPID(strconv.Itoa(pid))
	comm, err := os.ReadFile(filepath.Join(proc, "comm"))
	if err != nil || strings.TrimSpace(string(comm)) != "valkey-server" {
		return
	}
	// The real server is a valkey-server of the same user, and a recycled pid
	// may be its. Only a candidate has its directory in its command line.
	if cmdline, err := os.ReadFile(filepath.Join(proc, "cmdline")); err != nil || !bytes.Contains(cmdline, []byte(dir)) {
		return
	}
	if !ownedBy(filepath.Join(proc, "status"), uid) {
		return
	}
	d.Logf("Stopping a candidate Valkey server (pid %d) that an earlier job left running", pid)
	_ = killProcess(pid, syscall.SIGKILL)
}

// ownedBy reports whether a /proc status file says the process's real user is
// uid.
func ownedBy(status string, uid int) bool {
	b, err := os.ReadFile(status)
	if err != nil {
		return false
	}
	for line := range strings.Lines(string(b)) {
		if rest, ok := strings.CutPrefix(line, "Uid:"); ok {
			f := strings.Fields(rest)
			return len(f) > 0 && f[0] == strconv.Itoa(uid)
		}
	}
	return false
}

// enabled reports whether the service is enabled. A masked or disabled one is
// not a usable server, whatever answers on the port.
func enabled(ctx context.Context, d sysexec.Deps) bool {
	ctx, cancel := context.WithTimeout(ctx, reloadTimeout)
	defer cancel()
	res, err := d.Run.Run(ctx, sysexec.Cmd{Path: systemctl, Args: []string{"is-enabled", unit}})
	return err == nil && strings.TrimSpace(string(res.Stdout)) == "enabled"
}

// checkCandidate asks the candidate server, over its socket, what it loaded,
// and refuses it before the real server is touched: its default user must be
// off, it must have exactly the users the ACL file defines (the admin among
// them), and it must be bound to loopback and read Zimam's ACL file. Its port
// is not checked, since the test runs it with no port.
func checkCandidate(ctx context.Context, d sysexec.Deps, socket string, users []string) error {
	refuse := func(format string, args ...any) error {
		return &configtx.TestFailed{Output: "a server started from the files " + fmt.Sprintf(format, args...)}
	}
	out, err := cliSocket(ctx, d, socket, "ACL", "LIST")
	if err != nil {
		return refuse("didn't answer ACL LIST: %v", err)
	}
	var got []string
	defaultOff := false
	for line := range strings.Lines(out) {
		f := strings.Fields(line)
		if len(f) >= 2 && f[0] == "user" {
			got = append(got, f[1])
		}
		if len(f) >= 3 && f[0] == "user" && f[1] == "default" {
			defaultOff = f[2] == "off"
		}
	}
	slices.Sort(got)
	switch {
	case !defaultOff:
		return refuse("has the default user on")
	case !slices.Contains(got, adminUser):
		return refuse("has no %s user", adminUser)
	case !slices.Equal(got, users):
		return refuse("has the users %v, want %v", got, users)
	}
	out, err = cliSocket(ctx, d, socket, "CONFIG", "GET", "bind", "aclfile")
	if err != nil {
		return refuse("didn't answer CONFIG GET: %v", err)
	}
	cfg := map[string]string{}
	lines := strings.Split(out, "\n")
	for i := 0; i+1 < len(lines); i += 2 {
		cfg[lines[i]] = strings.TrimRight(lines[i+1], "\r")
	}
	if cfg["bind"] != "127.0.0.1 -::1" {
		return refuse("is bound to %q, want 127.0.0.1 -::1", cfg["bind"])
	}
	if cfg["aclfile"] != d.Paths.ValkeyACL() {
		return refuse("reads the ACL file %q, want %s", cfg["aclfile"], d.Paths.ValkeyACL())
	}
	return nil
}
