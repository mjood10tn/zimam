package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"os/user"
	"strconv"
	"syscall"
	"time"

	"zimam/agent/internal/audit"
	"zimam/agent/internal/commands"
	"zimam/agent/internal/config"
	"zimam/agent/internal/paths"
	"zimam/agent/internal/server"
	"zimam/agent/internal/sysexec"
)

// serve is `zimam-agent serve`. systemd starts it through
// zimam-agent.socket and passes it the listening socket (design §5.1).
func serve(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) > 0 {
		fmt.Fprintln(stderr, "usage: zimam-agent serve")
		return exitUsage
	}
	fail := func(err error) int {
		fmt.Fprintf(stderr, "zimam-agent serve: %v\n", err)
		return 1
	}
	// The socket comes first: run by hand, serve stops here without touching
	// anything.
	ln, err := server.Activated()
	if err != nil {
		return fail(err)
	}
	p := paths.Paths{}
	cfg, err := config.Load(p)
	if err != nil {
		return fail(err)
	}
	// /run is a tmpfs, so after a reboot the locks directory is gone until
	// something makes it again. Audit writes and job locks need it.
	if err := os.MkdirAll(p.LocksDir(), 0o700); err != nil {
		return fail(err)
	}
	zimam, zimamExists, err := zimamUID(user.Lookup)
	if err != nil {
		return fail(err)
	}
	log.SetFlags(0) // the journal adds timestamps
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()
	m := jobManager(p, cfg, -1) // serve reads job logs; only job run writes them
	// Jobs whose units ended while the agent was down (§5.7). If this fails,
	// those jobs wait for the next reconcile and the agent serves anyway.
	if n, err := m.Reconcile(ctx); err != nil {
		log.Printf("reconciling jobs: %v", err)
	} else if n > 0 {
		log.Printf("reconciled %d jobs", n)
	}
	// The read commands' view of the system. system.status reads agent.json
	// itself on each call, since jobs change it while serve runs.
	d := sysexec.Deps{Paths: p, Run: sysexec.ExecRunner{}, Logf: log.Printf, Now: time.Now}
	s := &server.Server{
		Listener:       ln,
		Allowed:        server.AllowRootAndZimam(zimam, zimamExists),
		Audit:          audit.Writer{Paths: p},
		Handlers:       commands.New(d, m),
		RequestTimeout: 10 * time.Second, // §5.2
		DirectTimeout:  30 * time.Second, // §5.2
	}
	if err := s.Serve(ctx); err != nil {
		return fail(err)
	}
	return 0
}

// zimamUID looks up the panel's user. Until zimam exists, only root may
// connect (§5.1).
func zimamUID(lookup func(name string) (*user.User, error)) (uid uint32, exists bool, err error) {
	u, err := lookup("zimam")
	if errors.As(err, new(user.UnknownUserError)) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	n, err := strconv.ParseUint(u.Uid, 10, 32)
	if err != nil {
		return 0, false, fmt.Errorf("user zimam has uid %q: %w", u.Uid, err)
	}
	return uint32(n), true, nil
}
