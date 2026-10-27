package sysexec

import (
	"context"
	"errors"
	"os"
	"time"

	"golang.org/x/sys/unix"
)

// lockPoll is how often Lock retries a held lock. A blocking flock can't
// be cancelled, so Lock polls with LOCK_NB until ctx is done.
const lockPoll = 50 * time.Millisecond

// Lock takes an exclusive flock on path (created 0600), for the reload and
// subject locks in §5.5 and §5.7. It waits until the lock is free or ctx is
// done. The lock is released by unlock or when the process exits.
func Lock(ctx context.Context, path string) (unlock func(), err error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	for {
		err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			return func() {
				unix.Flock(int(f.Fd()), unix.LOCK_UN)
				f.Close()
			}, nil
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EINTR) {
			f.Close()
			return nil, &os.PathError{Op: "flock", Path: path, Err: err}
		}
		select {
		case <-ctx.Done():
			f.Close()
			return nil, ctx.Err()
		case <-time.After(lockPoll):
		}
	}
}
