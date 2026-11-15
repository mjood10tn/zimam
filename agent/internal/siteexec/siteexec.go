// Package siteexec runs the fixed programs a deploy needs (git, Composer,
// PHP, artisan, ssh-keygen and the deploy script) as the site user, through
// the helper's exec operation (R02-D1). Root never runs site code.
package siteexec

import (
	"context"
	"io"

	"zimam/agent/contract"
	"zimam/agent/internal/helper"
	"zimam/agent/internal/sysexec"
)

// Request is one program run. Every argument is built by the agent from
// validated values; the panel never sends argv.
type Request struct {
	Program Program
	PHP     contract.PHPVersion // php, artisan and composer only
	// Args and Env travel in the helper's argv, readable by any local user
	// in /proc/<pid>/cmdline: never put a secret in them.
	Args  []string
	Dir   string            // relative to the site user's home; "" is the home
	Env   map[string]string // the program's allowlisted keys only
	Stdin io.Reader         // the deploy script; nil (empty) for the others
	// LogPrefix starts each output line in the job log.
	LogPrefix string
	// Capture, with CaptureMax > 0, takes the raw stdout instead of the job
	// log (spike amendment A8: git archive, rev-parse, the public key);
	// stderr is still logged. More than CaptureMax bytes fails the run.
	Capture    io.Writer
	CaptureMax int64
}

// Run runs r as the account a and logs its output lines through d.Logf. It
// returns the exit code; a non-zero exit also returns a *sysexec.ExitError,
// so a failed program can't pass for success. Cancelling ctx kills the
// program's process group.
func Run(ctx context.Context, d sysexec.Deps, h helper.Client, a helper.Account, r Request) (exitCode int, err error) {
	code, err := h.Exec(ctx, a, helper.ExecRequest{
		Program:    string(r.Program),
		PHP:        r.PHP,
		Args:       r.Args,
		Dir:        r.Dir,
		Env:        r.Env,
		Stdin:      r.Stdin,
		Capture:    r.Capture,
		CaptureMax: r.CaptureMax,
		Log:        logTo(d, r.LogPrefix),
	})
	return outcome(r.Program, code, err)
}

// logTo writes each line to the job log after prefix; a line is never a
// format string.
func logTo(d sysexec.Deps, prefix string) func(string) {
	return func(line string) { d.Logf("%s%s", prefix, line) }
}

func outcome(p Program, code int, err error) (int, error) {
	if err != nil {
		return code, err
	}
	if code != 0 {
		return code, &sysexec.ExitError{Path: string(p), Code: code}
	}
	return 0, nil
}
