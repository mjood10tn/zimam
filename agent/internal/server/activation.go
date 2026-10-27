package server

import (
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
)

// listenFDsStart is the first fd systemd passes (SD_LISTEN_FDS_START).
const listenFDsStart = 3

// Activated returns the listening socket systemd passes to serve through
// zimam-agent.socket (§5.1). Like sd_listen_fds, it unsets LISTEN_PID,
// LISTEN_FDS and LISTEN_FDNAMES, so child processes don't inherit them.
func Activated() (*net.UnixListener, error) {
	return activated(os.Getenv, os.Unsetenv, os.Getpid(), listenFDsStart)
}

func activated(getenv func(string) string, unsetenv func(string) error, pid, fd int) (*net.UnixListener, error) {
	listenPID, listenFDs := getenv("LISTEN_PID"), getenv("LISTEN_FDS")
	for _, name := range []string{"LISTEN_PID", "LISTEN_FDS", "LISTEN_FDNAMES"} {
		if err := unsetenv(name); err != nil {
			return nil, err
		}
	}
	switch {
	case listenPID == "" && listenFDs == "":
		return nil, errors.New("LISTEN_PID and LISTEN_FDS are unset: serve takes its socket from systemd, so start zimam-agent.socket instead of running serve by hand")
	case listenPID != strconv.Itoa(pid):
		return nil, fmt.Errorf("LISTEN_PID is %q, but this process is %d", listenPID, pid)
	case listenFDs != "1":
		return nil, fmt.Errorf("LISTEN_FDS is %q, want 1", listenFDs)
	}
	f := os.NewFile(uintptr(fd), "zimam-agent.socket")
	ln, err := net.FileListener(f)
	f.Close() // the listener holds its own copy of the fd
	if err != nil {
		return nil, fmt.Errorf("the socket from systemd (fd %d): %w", fd, err)
	}
	ul, ok := ln.(*net.UnixListener)
	if !ok {
		ln.Close()
		return nil, fmt.Errorf("the socket from systemd (fd %d) is not a Unix socket", fd)
	}
	return ul, nil
}
