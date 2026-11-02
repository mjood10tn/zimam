// Package sysexec runs programs and takes file locks for the agent. Every
// package that touches the system takes a Deps and runs commands through its
// Runner, never os/exec directly.
package sysexec

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os/exec"
	"path/filepath"
	"time"

	"zimam/agent/internal/config"
	"zimam/agent/internal/paths"
)

// baseEnv is the whole environment of every command, before Cmd.Env.
var baseEnv = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LANG=C.UTF-8"}

// Cmd is one program to run. It never goes through a shell; Path must be
// absolute; secrets travel only in Stdin, never in Args or Env.
type Cmd struct {
	Path  string
	Args  []string
	Env   []string // added to PATH=/usr/sbin:/usr/bin:/sbin:/bin and LANG=C.UTF-8
	Stdin io.Reader
	Dir   string
}

type Result struct {
	Stdout, Stderr []byte
	ExitCode       int
}

// Runner runs one program. A non-zero exit returns the Result together with
// an *ExitError, so a failure can't be mistaken for success; callers that
// expect a non-zero code read it from the Result.
type Runner interface {
	Run(ctx context.Context, c Cmd) (Result, error)
}

// ExitError reports a program that ran and exited non-zero.
type ExitError struct {
	Path   string
	Code   int
	Stderr []byte
}

func (e *ExitError) Error() string {
	msg := fmt.Sprintf("%s exited with code %d", e.Path, e.Code)
	if s := bytes.TrimSpace(e.Stderr); len(s) > 0 {
		const tail = 500 // the end of stderr usually holds the reason
		if len(s) > tail {
			s = s[len(s)-tail:]
		}
		msg += ": " + string(s)
	}
	return msg
}

func checkPath(path string) error {
	if !filepath.IsAbs(path) {
		return fmt.Errorf("sysexec: %q is not an absolute path", path)
	}
	return nil
}

// Output caps. Callers parse stdout whole, so more than maxStdout fails the
// run rather than being cut silently. Stderr only explains a failure, so its
// last maxStderr bytes are kept. Variables, so tests can lower them.
var (
	maxStdout = 64 << 20
	maxStderr = 1 << 20
)

// ErrOutputTooLarge reports a program whose stdout passed maxStdout.
var ErrOutputTooLarge = errors.New("output too large")

// headBuffer keeps the first max bytes written to it. It accepts the rest
// without keeping it, so the program isn't stopped by a broken pipe.
type headBuffer struct {
	buf  bytes.Buffer
	max  int
	over bool
}

func (b *headBuffer) Write(p []byte) (int, error) {
	n := len(p)
	if room := b.max - b.buf.Len(); len(p) > room {
		b.over = true
		p = p[:max(room, 0)]
	}
	b.buf.Write(p)
	return n, nil
}

// tailBuffer keeps the last max bytes written to it.
type tailBuffer struct {
	buf []byte
	max int
}

func (b *tailBuffer) Write(p []byte) (int, error) {
	n := len(p)
	if len(p) >= b.max {
		b.buf = append(b.buf[:0], p[len(p)-b.max:]...)
		return n, nil
	}
	if drop := len(b.buf) + len(p) - b.max; drop > 0 {
		b.buf = b.buf[:copy(b.buf, b.buf[drop:])]
	}
	b.buf = append(b.buf, p...)
	return n, nil
}

// ExecRunner is the Runner that starts real processes.
type ExecRunner struct{}

func (ExecRunner) Run(ctx context.Context, c Cmd) (Result, error) {
	if err := checkPath(c.Path); err != nil {
		return Result{}, err
	}
	cmd := exec.CommandContext(ctx, c.Path, c.Args...)
	cmd.Env = append(append([]string{}, baseEnv...), c.Env...)
	cmd.Stdin = c.Stdin
	cmd.Dir = c.Dir
	cmd.WaitDelay = 5 * time.Second // don't hang on pipes a killed child's children still hold
	stdout, stderr := &headBuffer{max: maxStdout}, &tailBuffer{max: maxStderr}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	err := cmd.Run()
	res := Result{Stdout: stdout.buf.Bytes(), Stderr: stderr.buf}
	if cmd.ProcessState != nil {
		res.ExitCode = cmd.ProcessState.ExitCode()
	}
	if ctx.Err() != nil {
		return res, ctx.Err()
	}
	if stdout.over {
		return res, fmt.Errorf("sysexec: %s wrote more than %d bytes: %w", c.Path, maxStdout, ErrOutputTooLarge)
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return res, &ExitError{Path: c.Path, Code: res.ExitCode, Stderr: res.Stderr}
	}
	return res, err
}

// Deps is what every system-touching package takes (php, nginx, systemd,
// logpolicy, acme, sites, checks, logs, maintenance, bootstrap). Packages
// that need the helper or the audit writer take them as extra arguments.
type Deps struct {
	Paths  paths.Paths
	Run    Runner
	Config config.Agent
	Logf   func(format string, args ...any) // the job log, or stderr for CLI runs
	Now    func() time.Time
	HTTP   *http.Client // downloads; nil means defaultHTTP
	DNS    Resolver     // nil means net.DefaultResolver
}

// Resolver looks up a host's addresses, as *net.Resolver does.
type Resolver interface {
	LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error)
}

// DNSResolver returns the resolver lookups use.
func (d Deps) DNSResolver() Resolver {
	if d.DNS != nil {
		return d.DNS
	}
	return net.DefaultResolver
}

// defaultHTTP is the client for downloads when Deps.HTTP is nil. Its timeout
// covers the whole request, body included.
var defaultHTTP = &http.Client{Timeout: 5 * time.Minute}

// HTTPClient returns the client downloads use.
func (d Deps) HTTPClient() *http.Client {
	if d.HTTP != nil {
		return d.HTTP
	}
	return defaultHTTP
}
