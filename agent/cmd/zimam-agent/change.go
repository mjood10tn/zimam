package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"zimam/agent/contract"
	"zimam/agent/internal/audit"
	"zimam/agent/internal/changes"
	"zimam/agent/internal/config"
	"zimam/agent/internal/sysexec"
)

// changeRevert is `zimam-agent change revert <id>`, which a guarded
// change's timer runs when the owner didn't confirm it in time (R06-D1).
func changeRevert(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) != 1 || contract.ChangeID(args[0]).Validate() != nil {
		fmt.Fprintln(stderr, "usage: zimam-agent change revert <id>")
		return exitUsage
	}
	p := systemPaths()
	cfg, err := config.Load(p)
	if err != nil {
		fmt.Fprintf(stderr, "zimam-agent change revert: %v\n", err)
		return 1
	}
	if err := os.MkdirAll(p.LocksDir(), 0o700); err != nil {
		fmt.Fprintf(stderr, "zimam-agent change revert: %v\n", err)
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
	return runChangeRevert(ctx, d, audit.Writer{Paths: p}, contract.ChangeID(args[0]), stderr)
}

// runChangeRevert reverts the change and audits it as cli:root (global
// rule 7).
func runChangeRevert(ctx context.Context, d sysexec.Deps, w audit.Writer, id contract.ChangeID, stderr io.Writer) int {
	start := d.Now()
	err := changes.Revert(ctx, d, id)
	outcome := "ok"
	var ae *contract.AgentError
	switch {
	case errors.As(err, &ae):
		outcome = "error:" + string(ae.Code)
	case err != nil:
		outcome = "error:" + string(contract.ErrInternal)
	}
	aerr := w.Write(audit.Entry{
		TS:         start,
		Actor:      "cli:root",
		Command:    "change revert",
		Params:     audit.Redact(contract.ChangeConfirmParams{Change: id}),
		Outcome:    outcome,
		DurationMS: d.Now().Sub(start).Milliseconds(),
	})
	if err = errors.Join(err, aerr); err != nil {
		fmt.Fprintf(stderr, "zimam-agent change revert: %v\n", err)
		return 1
	}
	return 0
}
