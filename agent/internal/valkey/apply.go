package valkey

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"strconv"
	"strings"

	"zimam/agent/contract"
	"zimam/agent/internal/configtx"
	"zimam/agent/internal/services"
	"zimam/agent/internal/sysexec"
)

// renderUsers makes the users file. It is RenderACL; tests replace it to make
// the file a server refuses.
var renderUsers = RenderACL

// Installed reports whether Zimam installed Valkey on this server.
func Installed(d sysexec.Deps) (bool, error) {
	st, err := services.Load(d.Paths)
	if err != nil {
		return false, err
	}
	return st.Valkey.Installed, nil
}

// Ping asks the running server, as the admin, whether it answers.
func Ping(ctx context.Context, d sysexec.Deps) error {
	out, err := cliAdmin(ctx, d, "PING")
	if err != nil {
		return err
	}
	if out != "PONG" {
		return fmt.Errorf("Valkey answered PING with %q", out)
	}
	return nil
}

// aclLoad tells the server to read its users file again. The server keeps
// its previous users if the file is wrong, and says so.
func aclLoad(ctx context.Context, d sysexec.Deps) error {
	out, err := cliAdmin(ctx, d, "ACL", "LOAD")
	if err != nil {
		return err
	}
	if out != "OK" {
		return fmt.Errorf("ACL LOAD was answered %q", out)
	}
	return nil
}

// KillUserConnections closes every connection of a site's user and returns
// how many it closed. A server keeps the connections of a user whose
// password changed, so this is what makes the old password stop working
// at once. A user the server doesn't have has no connection, which is no
// error: a removed user's connections are closed by the server itself.
func KillUserConnections(ctx context.Context, d sysexec.Deps, site contract.SiteName) (int, error) {
	if err := site.Validate(); err != nil {
		return 0, fmt.Errorf("site %q %v", site, err)
	}
	out, err := cliAdmin(ctx, d, "CLIENT", "KILL", "USER", "s_"+string(site))
	if err != nil {
		if strings.Contains(err.Error(), "No such user") {
			return 0, nil
		}
		return 0, err
	}
	n, err := strconv.Atoi(out)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("CLIENT KILL USER was answered %q", out)
	}
	return n, nil
}

// ApplyUsers makes the live server's users the ones the site states hold:
// it renders the users file, writes it through configtx and runs ACL LOAD.
// It holds the lock valkey.install holds, so the file is never written from a
// list another change has outdated.
//
// users lists the sites' users; it is called under the lock. change, if not
// nil, is the state change the file must follow: it runs under the same hold
// of the lock and before users, so no render can read the states between the
// change and the load. It returns a revert that puts the state back. If the
// file can't be made or Valkey refuses it, the previous file is put back and
// loaded again, then revert runs, so state, file and the live users agree
// again; the error says what happened to each. The restore and its load are not
// cut by the job's cancellation, and are bounded.
//
// A process that is killed between the change and the load leaves the state
// ahead of the file; the next apply or install renders from the state.
func ApplyUsers(ctx context.Context, d sysexec.Deps, users func() ([]SiteUser, error), change func() (revert func() error, err error)) error {
	p := d.Paths
	if err := os.MkdirAll(p.LocksDir(), 0o700); err != nil {
		return err
	}
	lockCtx, cancel := context.WithTimeout(ctx, lockWait)
	unlock, err := sysexec.Lock(lockCtx, p.ValkeyACLLock())
	timedOut := lockCtx.Err() != nil && ctx.Err() == nil
	cancel()
	if err != nil {
		if timedOut {
			return fmt.Errorf("Valkey's users are being changed by another job, which can take several minutes after an install; try again shortly: %w", err)
		}
		return fmt.Errorf("waiting for the lock on Valkey's users: %w", err)
	}
	defer unlock()

	pass, found, err := readAdminPassword(d)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("%s has no admin password; install Valkey first", p.ValkeyAdminPass())
	}
	if _, err := os.Lstat(p.ValkeyACL()); err != nil {
		return fmt.Errorf("Valkey's users file can't be read (%v); install Valkey first", err)
	}
	gid, err := idOf(p.EtcGroup(), serviceUser)
	if err != nil {
		return err
	}

	var revert func() error
	if change != nil {
		if revert, err = change(); err != nil {
			return err
		}
	}
	fail := func(err error) error {
		if revert == nil {
			return err
		}
		if rerr := revert(); rerr != nil {
			return fmt.Errorf("%w; putting the site's state back failed too, so the state and Valkey's users are out of step: %v", err, rerr)
		}
		return fmt.Errorf("%w; the site's state is back as it was", err)
	}

	siteUsers, err := users()
	if err != nil {
		return fail(err)
	}
	acl, err := renderUsers(sha256.Sum256([]byte(pass)), siteUsers)
	if err != nil {
		return fail(err)
	}

	passes := 0
	var loadErr, restoreErr error
	restored := false
	err = configtx.Apply(ctx, []configtx.Change{{Path: p.ValkeyACL(), Content: acl, Mode: 0o640, UID: -1, GID: gid}}, func(ctx context.Context) error {
		// Apply runs this again after it put the file back, and also when it
		// couldn't stage the file. Only the first run with the new file in
		// place asks Valkey to load it; the next one loads what is on disk.
		on, readErr := os.ReadFile(p.ValkeyACL())
		forward := passes == 0 && readErr == nil && bytes.Equal(on, acl)
		passes++
		if forward {
			loadErr = aclLoad(ctx, d)
			return loadErr
		}
		// A job is cancelled by ending ctx; the users must still be put back.
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*cliTimeout)
		defer cancel()
		restored = true
		restoreErr = aclLoad(ctx, d)
		return restoreErr
	})
	if err == nil {
		return nil
	}
	switch {
	case loadErr != nil && restoreErr == nil && restored:
		err = fmt.Errorf("ACL LOAD refused the new users file: %v. The previous users file was put back and loaded again", loadErr)
	case loadErr != nil && restoreErr != nil:
		err = fmt.Errorf("ACL LOAD refused the new users file: %v. The previous users file was put back, but loading it failed too (%v), so Valkey may be running users that are not in %s", loadErr, restoreErr, p.ValkeyACL())
	case restoreErr != nil:
		err = fmt.Errorf("%w; the previous users file was put back, but loading it failed (%v), so Valkey may be running users that are not in %s", err, restoreErr, p.ValkeyACL())
	}
	return fail(err)
}
