package valkey

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"zimam/agent/internal/sysexec"
)

const (
	cliPath   = "/usr/bin/valkey-cli"
	adminUser = "zimam-admin"
)

// cliTimeout bounds one command, so a server that accepts connections but
// never answers can't hold a job until its time limit.
var cliTimeout = 10 * time.Second

// cliAdmin is the one way the agent talks to Valkey: valkey-cli as the admin user,
// to the server on 127.0.0.1:6379, with the admin password in REDISCLI_AUTH.
// The environment is the one place a password may travel besides stdin; it
// never goes in argv, a log line or an error. The arguments are fixed words
// from the agent's own code and values that passed their contract type's
// Validate, never text from the panel. It returns what the command printed.
//
// valkey-cli with -e exits non-zero on an error reply, and the reply text is
// checked too, so an error never passes for a result.
//
// Only this package's typed functions (Ping, aclLoad, KillUserConnections and
// the install's checks) call it, so no text from the panel reaches it.
func cliAdmin(ctx context.Context, d sysexec.Deps, args ...string) (string, error) {
	return cli(ctx, d, []string{"-h", "127.0.0.1", "-p", "6379"}, args)
}

// cliAdminDB is cliAdmin with database db selected for the call (valkey-cli -n),
// which database 0, the default, needs no option for.
func cliAdminDB(ctx context.Context, d sysexec.Deps, db int, args ...string) (string, error) {
	target := []string{"-h", "127.0.0.1", "-p", "6379"}
	if db != 0 {
		target = append(target, "-n", strconv.Itoa(db))
	}
	return cli(ctx, d, target, args)
}

// cliSocket is cliAdmin for a server that listens on a unix socket only, which is
// how the config test's candidate server runs.
func cliSocket(ctx context.Context, d sysexec.Deps, socket string, args ...string) (string, error) {
	return cli(ctx, d, []string{"-s", socket}, args)
}

func cli(ctx context.Context, d sysexec.Deps, target, args []string) (string, error) {
	pass, found, err := readAdminPassword(d)
	if err != nil {
		return "", err
	}
	if !found {
		return "", fmt.Errorf("%s has no admin password; valkey.install makes it", d.Paths.ValkeyAdminPass())
	}
	ctx, cancel := context.WithTimeout(ctx, cliTimeout)
	defer cancel()
	argv := append([]string{"-e", "--no-auth-warning", "--user", adminUser}, target...)
	res, err := d.Run.Run(ctx, sysexec.Cmd{Path: cliPath, Args: append(argv, args...), Env: []string{"REDISCLI_AUTH=" + pass}})
	out := strings.TrimSpace(string(res.Stdout))
	if err != nil {
		var exit *sysexec.ExitError
		if !errors.As(err, &exit) {
			// The client didn't start or the context ended: the error holds a
			// path or a code, never the environment.
			return "", fmt.Errorf("valkey-cli: %w", err)
		}
		return "", fmt.Errorf("valkey-cli: %s", reason(pass, out, string(res.Stderr), exit.Code))
	}
	if errorReply.MatchString(out) {
		return "", fmt.Errorf("valkey-cli: %s", reason(pass, out, "", 0))
	}
	return out, nil
}

// errorReply is how a reply that is an error begins. valkey-cli exits 0 for
// some of them.
var errorReply = regexp.MustCompile(`^(\(error\) )?(ERR|NOPERM|WRONGPASS|NOAUTH|AUTH failed|LOADING|BUSY|MISCONF|READONLY|EXECABORT|WRONGTYPE|NOSCRIPT|NOREPLICAS|MASTERDOWN|CLUSTERDOWN|TRYAGAIN|OOM|NOPROTO|UNKILLABLE)\b`)

// reason is what a failed command printed, with the password taken out in
// case a server or the client quotes it, and cut to its end, where the
// reason usually is.
func reason(pass, stdout, stderr string, code int) string {
	text := strings.TrimSpace(stdout + "\n" + stderr)
	text = strings.TrimSpace(strings.ReplaceAll(text, pass, "***"))
	const tail = 500
	if len(text) > tail {
		text = text[len(text)-tail:]
	}
	if text == "" {
		return fmt.Sprintf("exited with code %d", code)
	}
	return text
}

// adminPasswordRe is what Zimam generates: 32 random bytes in base64url.
var adminPasswordRe = regexp.MustCompile(`^[A-Za-z0-9_-]{43}\z`)

// readAdminPassword reads admin.pass. A file that isn't there, or isn't a
// password Zimam made, is not found; a link or something that isn't a plain
// file is an error, since root must not follow it.
func readAdminPassword(d sysexec.Deps) (pass string, found bool, err error) {
	path := d.Paths.ValkeyAdminPass()
	fi, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if fi.Mode()&fs.ModeSymlink != 0 {
		return "", false, fmt.Errorf("%s is a symbolic link; Zimam won't follow it", path)
	}
	if !fi.Mode().IsRegular() {
		return "", false, fmt.Errorf("%s is not a regular file", path)
	}
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return "", false, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 256))
	if err != nil {
		return "", false, err
	}
	pass = strings.TrimRight(string(data), "\r\n")
	if !adminPasswordRe.MatchString(pass) {
		return "", false, nil
	}
	return pass, true, nil
}
