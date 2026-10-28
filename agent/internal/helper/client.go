// Package helper is the site-user helper (design §5.6). Root never opens a
// path inside /home/zimam or /home/s_<name> itself (D5): Client runs
// `zimam-agent as-user` as the account that owns the home, and that process
// does the work through an os.Root rooted at the home.
package helper

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os/exec"
	"slices"
	"strconv"
	"syscall"
	"time"

	"zimam/agent/contract"
)

// Account is a user the helper runs as: zimam or a site user.
type Account struct {
	Name   string
	UID    uint32
	GID    uint32
	Groups []uint32 // the account's own supplementary groups, never root's
	Home   string
}

// Client runs helper operations. Each method starts one `zimam-agent
// as-user` process with the account's credentials and decodes the JSON line
// it prints. Paths are relative to the account's home.
type Client struct {
	AgentBinary string // the agent itself (paths.AgentBinary)

	prepare func(*exec.Cmd) // tests only: adjusts each command before it runs
}

// Limits on what the client reads back from one helper process.
const (
	maxStdout = 64 << 20
	maxStderr = 64 << 10
)

// command builds the process that runs one helper operation as a. Go can't
// switch users for part of a process, so the agent runs itself again (§5.6).
func (c *Client) command(ctx context.Context, a Account, op string, args ...string) *exec.Cmd {
	argv := append([]string{"as-user", "--home", a.Home, op}, args...)
	cmd := exec.CommandContext(ctx, c.AgentBinary, argv...)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Credential: &syscall.Credential{Uid: a.UID, Gid: a.GID, Groups: a.Groups},
		Setsid:     true,
	}
	cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + a.Home}
	cmd.Dir = "/"
	return cmd
}

// run runs op as a, with stdin as its input, and decodes the one JSON line
// it prints into out.
func (c *Client) run(ctx context.Context, a Account, op string, stdin io.Reader, out any, args ...string) error {
	if a.UID == 0 || a.GID == 0 || slices.Contains(a.Groups, 0) {
		return fmt.Errorf("as-user %s as %s: refusing an account with root's uid, gid or group", op, a.Name)
	}
	cmd := c.command(ctx, a, op, args...)
	if c.prepare != nil {
		c.prepare(cmd)
	}
	stdout, stderr := &capped{max: maxStdout}, &capped{max: maxStderr}
	cmd.Stdin, cmd.Stdout, cmd.Stderr = stdin, stdout, stderr
	cmd.WaitDelay = 5 * time.Second
	err := cmd.Run()
	if ctx.Err() != nil {
		return fmt.Errorf("as-user %s as %s: %w", op, a.Name, ctx.Err())
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return &opError{op: op, account: a.Name, code: exitErr.ExitCode(), stderr: stderr.buf.Bytes()}
	}
	if err != nil {
		return fmt.Errorf("as-user %s as %s: %w", op, a.Name, err)
	}
	line, ok := bytes.CutSuffix(stdout.buf.Bytes(), []byte("\n"))
	if stdout.over || !ok || bytes.IndexByte(line, '\n') >= 0 {
		return fmt.Errorf("as-user %s as %s: the output is not one JSON line of at most %d bytes", op, a.Name, maxStdout)
	}
	if err := contract.DecodeStrict(line, out); err != nil {
		return fmt.Errorf("as-user %s as %s: decoding the output: %w", op, a.Name, err)
	}
	return nil
}

// capped keeps the first max bytes written to it and drops the rest without
// failing, so the helper never blocks on a full pipe.
type capped struct {
	buf  bytes.Buffer
	max  int
	over bool
}

func (w *capped) Write(p []byte) (int, error) {
	if room := w.max - w.buf.Len(); len(p) > room {
		w.buf.Write(p[:room])
		w.over = true
		return len(p), nil
	}
	return w.buf.Write(p)
}

// opError is a helper process that exited non-zero, with what it wrote to
// stderr. Exit code 3 means the path doesn't exist.
type opError struct {
	op, account string
	code        int
	stderr      []byte
}

func (e *opError) Error() string {
	msg := bytes.TrimSpace(e.stderr)
	const tail = 500 // the end of stderr holds the reason
	if len(msg) > tail {
		msg = msg[len(msg)-tail:]
	}
	if len(msg) == 0 {
		return fmt.Sprintf("as-user %s as %s: exited with code %d", e.op, e.account, e.code)
	}
	return fmt.Sprintf("as-user %s as %s: %s", e.op, e.account, msg)
}

func (e *opError) Unwrap() error {
	if e.code == exitNotExist {
		return fs.ErrNotExist
	}
	return nil
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func octal(mode fs.FileMode) string { return strconv.FormatUint(uint64(mode), 8) }

// Mkdir creates the directory name, and any missing parents, and gives it
// mode.
func (c *Client) Mkdir(ctx context.Context, a Account, name string, mode fs.FileMode) error {
	return c.run(ctx, a, "mkdir", nil, &struct{}{}, name, octal(mode))
}

// Write replaces name with content, at most MaxWriteBytes, with mode: it
// writes a temporary file and renames it into place.
func (c *Client) Write(ctx context.Context, a Account, name string, mode fs.FileMode, content io.Reader) error {
	return c.run(ctx, a, "write", content, &struct{}{}, name, octal(mode))
}

// Read reads at most limit bytes, and never more than MaxReadBytes, from
// offset in the regular file name.
func (c *Client) Read(ctx context.Context, a Account, name string, offset, limit int64) (ReadResult, error) {
	var r ReadResult
	err := c.run(ctx, a, "read", nil, &r, name, itoa(offset), itoa(limit))
	return r, err
}

// Stat describes name without following it; a symlink comes with its target.
func (c *Client) Stat(ctx context.Context, a Account, name string) (FileInfo, error) {
	var fi FileInfo
	err := c.run(ctx, a, "stat", nil, &fi, name)
	return fi, err
}

// List describes dir's entries, at most MaxListEntries, without following
// symlinks.
func (c *Client) List(ctx context.Context, a Account, dir string) (Listing, error) {
	var l Listing
	err := c.run(ctx, a, "list", nil, &l, dir)
	return l, err
}

// SymlinkSwap points link at target, releases/<name>, by renaming a new
// symlink over it.
func (c *Client) SymlinkSwap(ctx context.Context, a Account, link, target string) error {
	return c.run(ctx, a, "symlink-swap", nil, &struct{}{}, link, target)
}

// RemoveTree deletes name and everything under it; for "." it empties the
// home and keeps it.
func (c *Client) RemoveTree(ctx context.Context, a Account, name string) error {
	return c.run(ctx, a, "remove-tree", nil, &struct{}{}, name)
}

// ScanWorldWritable finds what anyone may write to under dir, looking at
// MaxScanEntries entries at most.
func (c *Client) ScanWorldWritable(ctx context.Context, a Account, dir string) (ScanResult, error) {
	var r ScanResult
	err := c.run(ctx, a, "scan-world-writable", nil, &r, dir)
	return r, err
}

// Tail returns the last lines (1 to MaxTailLines) of the regular file name
// that end at or before the byte offset before; a negative before means the
// end of the file.
func (c *Client) Tail(ctx context.Context, a Account, name string, before int64, lines int) (Lines, error) {
	var l Lines
	err := c.run(ctx, a, "tail", nil, &l, name, itoa(before), strconv.Itoa(lines))
	return l, err
}

// Search returns the lines of the regular file name, from the byte offset
// from on, that contain q in any case: at most limit (1 to
// MaxSearchMatches), within SearchTimeout.
func (c *Client) Search(ctx context.Context, a Account, name string, from int64, q contract.LogSearch, limit int) (SearchResult, error) {
	var r SearchResult
	err := c.run(ctx, a, "search", nil, &r, name, itoa(from), strconv.Itoa(limit), string(q))
	return r, err
}

// ExtractTar unpacks the gzip tarball into dest, which it creates, within
// lim (§4 bootstrap step 6).
func (c *Client) ExtractTar(ctx context.Context, a Account, dest string, tarball io.Reader, lim ExtractLimits) error {
	return c.run(ctx, a, "extract-tar", tarball, &struct{}{}, dest, itoa(lim.MaxBytes), strconv.Itoa(lim.MaxEntries))
}
