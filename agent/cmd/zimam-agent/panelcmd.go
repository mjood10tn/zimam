package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"zimam/agent/contract"
	"zimam/agent/internal/audit"
	"zimam/agent/internal/bootstrap"
	"zimam/agent/internal/config"
	"zimam/agent/internal/paths"
	"zimam/agent/internal/sysexec"
)

// panelSetupLink is `zimam-agent panel setup-link` (design §4): the
// recovery path over SSH. It prints a new setup link and the panel IP
// allowlist.
func panelSetupLink(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	p := paths.Paths{}
	cfg, err := config.Load(p)
	if err != nil {
		fmt.Fprintf(stderr, "zimam-agent panel setup-link: %v\n", err)
		return 1
	}
	// /run is a tmpfs, and the audit lock lives there.
	if err := os.MkdirAll(p.LocksDir(), 0o700); err != nil {
		fmt.Fprintf(stderr, "zimam-agent panel setup-link: %v\n", err)
		return 1
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
	return setupLink(ctx, d, audit.Writer{Paths: p}, args, stdout, stderr)
}

// setupLink runs zimam:setup-link as zimam and prints its output as it
// is. The audit entry records the flag and the outcome, never the link.
func setupLink(ctx context.Context, d sysexec.Deps, w audit.Writer, args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("panel setup-link", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	clear := fs.Bool("clear-ip-allowlist", false, "")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		fmt.Fprintln(stderr, "usage: zimam-agent panel setup-link [--clear-ip-allowlist]")
		return exitUsage
	}
	start := d.Now()
	out, _, err := bootstrap.SetupLink(ctx, d, *clear)
	outcome := "ok"
	if err != nil {
		outcome = "error:" + string(contract.ErrInternal)
	}
	aerr := w.Write(audit.Entry{
		TS:      start,
		Actor:   "cli:root",
		Command: "panel setup-link",
		Params: audit.Redact(struct {
			ClearIPAllowlist bool `json:"clear_ip_allowlist"`
		}{*clear}),
		Outcome:    outcome,
		DurationMS: d.Now().Sub(start).Milliseconds(),
	})
	if err == nil {
		err = aerr
	}
	if err != nil {
		fmt.Fprintf(stderr, "zimam-agent panel setup-link: %v\n", err)
		return 1
	}
	stdout.Write(out)
	return 0
}
