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
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	res := Result{Stdout: stdout.Bytes(), Stderr: stderr.Bytes()}
	if cmd.ProcessState != nil {
		res.ExitCode = cmd.ProcessState.ExitCode()
	}
	if ctx.Err() != nil {
		return res, ctx.Err()
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
}
