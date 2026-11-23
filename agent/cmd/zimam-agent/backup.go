package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"os/user"
	"syscall"
	"time"

	"zimam/agent/contract"
	"zimam/agent/internal/audit"
	"zimam/agent/internal/backup"
	"zimam/agent/internal/config"
	"zimam/agent/internal/helper"
	"zimam/agent/internal/sysexec"
)

// backupRun is `zimam-agent backup run <plan>`, which the plan's timer runs
// (R05-D7). It shares backup.run's code and logs to stderr, the journal
// under the timer.
func backupRun(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) != 1 || contract.PlanID(args[0]).Validate() != nil {
		fmt.Fprintln(stderr, "usage: zimam-agent backup run <plan>")
		return exitUsage
	}
	fail := func(err error) int {
		fmt.Fprintf(stderr, "zimam-agent backup run: %v\n", err)
		return 1
	}
	p := systemPaths()
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
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()
	d := sysexec.Deps{
		Paths:  p,
		Run:    sysexec.ExecRunner{},
		Config: cfg,
		Logf:   func(format string, args ...any) { fmt.Fprintf(stderr, format+"\n", args...) },
		Now:    time.Now,
	}
	env := backup.RunEnv{Helper: &helper.Client{AgentBinary: p.AgentBinary()}, Lookup: helper.LookupAccount, LogGID: gid}
	return runBackup(ctx, d, env, audit.Writer{Paths: p}, contract.PlanID(args[0]), stderr)
}

// runBackup runs the plan and audits the run as cli:root (global rule 7).
// A plan already running is skipped with exit 0: the timer fired during a
// long run, or the owner started one from the panel.
func runBackup(ctx context.Context, d sysexec.Deps, env backup.RunEnv, w audit.Writer, plan contract.PlanID, stderr io.Writer) int {
	start := d.Now()
	_, err := backup.RunPlan(ctx, d, env, plan, contract.TriggerTimer)
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
		Command:    "backup run",
		Params:     audit.Redact(contract.BackupRunParams{Plan: plan}),
		Outcome:    outcome,
		DurationMS: d.Now().Sub(start).Milliseconds(),
	})
	if ae != nil && ae.Code == contract.ErrBusy {
		fmt.Fprintf(stderr, "zimam-agent backup run: %s; this run is skipped\n", ae.Message)
		err = nil
	}
	if err == nil {
		err = aerr
	}
	if err != nil {
		fmt.Fprintf(stderr, "zimam-agent backup run: %v\n", err)
		return 1
	}
	return 0
}
