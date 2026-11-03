package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"zimam/agent/internal/audit"
	"zimam/agent/internal/config"
	"zimam/agent/internal/helper"
	"zimam/agent/internal/maintenance"
	"zimam/agent/internal/sysexec"
)

// runMaintenance is `zimam-agent maintenance`, which zimam-maintenance.timer
// runs every 15 minutes (design §11.3). It logs to stderr, which is the
// journal under the timer.
func runMaintenance(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) != 0 {
		fmt.Fprintln(stderr, "usage: zimam-agent maintenance")
		return exitUsage
	}
	fail := func(err error) int {
		fmt.Fprintf(stderr, "zimam-agent maintenance: %v\n", err)
		return 1
	}
	p := systemPaths()
	cfg, err := config.Load(p)
	if err != nil {
		return fail(err)
	}
	// /run is a tmpfs: after a reboot the timer may run before anything
	// else has made the locks directory.
	if err := os.MkdirAll(p.LocksDir(), 0o700); err != nil {
		return fail(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()
	d := sysexec.Deps{
		Paths:  p,
		Run:    sysexec.ExecRunner{},
		Config: cfg,
		Logf:   func(format string, args ...any) { fmt.Fprintf(stderr, format+"\n", args...) },
		Now:    time.Now,
	}
	if err := maintenance.Run(ctx, d, jobManager(p, cfg, -1), maintenance.Homes{Helper: &helper.Client{AgentBinary: p.AgentBinary()}, Lookup: helper.LookupAccount}, audit.Writer{Paths: p}); err != nil {
		return fail(err)
	}
	return 0
}
