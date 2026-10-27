package server

import (
	"net"

	"golang.org/x/sys/unix"
)

// peerUID returns the uid of the process on the other end of c, as the
// kernel recorded it at connect time (SO_PEERCRED).
func peerUID(c *net.UnixConn) (uint32, error) {
	raw, err := c.SyscallConn()
	if err != nil {
		return 0, err
	}
	var cred *unix.Ucred
	var credErr error
	if err := raw.Control(func(fd uintptr) {
		cred, credErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	}); err != nil {
		return 0, err
	}
	if credErr != nil {
		return 0, credErr
	}
	return cred.Uid, nil
}

// AllowRootAndZimam is the production caller check (§5.1). zimamUID comes
// from /etc/passwd at startup; if zimam doesn't exist only root is allowed.
func AllowRootAndZimam(zimamUID uint32, zimamExists bool) func(uint32) bool {
	return func(uid uint32) bool {
		return uid == 0 || (zimamExists && uid == zimamUID)
	}
}
