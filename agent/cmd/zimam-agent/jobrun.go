package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"os/user"
	"strconv"
	"syscall"

	"zimam/agent/contract"
	"zimam/agent/internal/audit"
	"zimam/agent/internal/commands"
	"zimam/agent/internal/config"
	"zimam/agent/internal/jobs"
	"zimam/agent/internal/paths"
	"zimam/agent/internal/sysexec"
)

// jobRun is `zimam-agent job run <id>`, the body of a job's transient unit
// (design §5.7).
func jobRun(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) != 1 || contract.JobID(args[0]).Validate() != nil {
		fmt.Fprintln(stderr, "usage: zimam-agent job run <job id>")
		return exitUsage
	}
	fail := func(err error) int {
		fmt.Fprintf(stderr, "zimam-agent job run: %v\n", err)
		return 1
	}
	p := paths.Paths{}
	cfg, err := config.Load(p)
	if err != nil {
		return fail(err)
	}
	if err := os.MkdirAll(p.LocksDir(), 0o700); err != nil {
		return fail(err)
	}
	gid, err := jobLogGID(user.LookupGroup)
	if err != nil {
		return fail(err)
	}
	// systemctl stop, and RuntimeMaxSec, end a job with SIGTERM.
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()
	if err := jobManager(p, cfg, gid).RunJob(ctx, contract.JobID(args[0])); err != nil {
		return fail(err)
	}
	return 0
}

// jobManager is the jobs.Manager of serve and job run.
func jobManager(p paths.Paths, cfg config.Agent, logGID int) *jobs.Manager {
	run := sysexec.ExecRunner{}
	return &jobs.Manager{
		Paths:    p,
		Systemd:  jobs.Systemctl{Run: run},
		Audit:    audit.Writer{Paths: p},
		Handlers: commands.JobHandlers(),
		Config:   cfg,
		Run:      run,
		LogGID:   logGID,
	}
}

// jobLogGID returns the gid of zimam-agent, the group of job logs (§5.1),
// or -1 while the group doesn't exist.
func jobLogGID(lookup func(name string) (*user.Group, error)) (int, error) {
	g, err := lookup("zimam-agent")
	if errors.As(err, new(user.UnknownGroupError)) {
		return -1, nil
	}
	if err != nil {
		return 0, err
	}
	gid, err := strconv.Atoi(g.Gid)
	if err != nil {
		return 0, fmt.Errorf("group zimam-agent has gid %q: %w", g.Gid, err)
	}
	return gid, nil
}
