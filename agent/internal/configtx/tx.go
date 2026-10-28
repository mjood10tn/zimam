// Package configtx applies the files root reads (design §5.5): stage every
// change, run the matching tool's test, and restore the previous files if
// anything fails.
package configtx

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"

	"zimam/agent/internal/paths"
	"zimam/agent/internal/sysexec"
)

// Change is one file to write, or to remove when Remove is set.
type Change struct {
	Path     string
	Content  []byte
	Mode     os.FileMode
	UID, GID int
	Remove   bool
}

// TestFailed carries the tester's output into the job's failure message.
type TestFailed struct{ Output string }

func (e *TestFailed) Error() string { return "configuration test failed:\n" + e.Output }

// Apply stages every change and keeps the result only if test passes (§5.5).
// On any failure it restores every previous file, removes files that didn't
// exist, runs test again, and returns the first failure.
func Apply(ctx context.Context, changes []Change, test func(context.Context) error) error {
	// A second change to one path would replace the first one's .zimam-prev,
	// and the original file could no longer be restored.
	seen := map[string]bool{}
	for _, c := range changes {
		p := filepath.Clean(c.Path)
		if seen[p] {
			return fmt.Errorf("%s is changed more than once", p)
		}
		seen[p] = true
	}
	var staged []*staging
	undo := func(cause error) error {
		for i := len(staged) - 1; i >= 0; i-- {
			if err := staged[i].restore(); err != nil {
				cause = fmt.Errorf("%w; restoring %s: %v", cause, staged[i].path, err)
			}
		}
		if err := test(ctx); err != nil {
			cause = fmt.Errorf("%w; the restored configuration also fails its test: %v", cause, err)
		}
		return cause
	}
	for _, c := range changes {
		s, err := stage(c)
		if s != nil {
			staged = append(staged, s)
		}
		if err != nil {
			return undo(err)
		}
	}
	if err := test(ctx); err != nil {
		return undo(err)
	}
	for _, s := range staged {
		s.commit()
	}
	return nil
}

type staging struct {
	path    string
	hadPrev bool
}

func stage(c Change) (*staging, error) {
	prev := c.Path + ".zimam-prev"
	s := &staging{path: c.Path}
	fi, err := os.Lstat(c.Path)
	switch {
	case err == nil && !fi.Mode().IsRegular():
		return nil, fmt.Errorf("%s is not a regular file", c.Path)
	case err == nil:
		_ = os.Remove(prev)
		if err := os.Link(c.Path, prev); err != nil {
			return nil, err
		}
		s.hadPrev = true
	case !errors.Is(err, fs.ErrNotExist):
		return nil, err
	}
	if c.Remove {
		if !s.hadPrev {
			return s, nil
		}
		return s, os.Remove(c.Path)
	}
	tmp := c.Path + ".zimam-tmp"
	_ = os.Remove(tmp)
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return s, err
	}
	err = writeAll(f, c)
	if err != nil {
		_ = os.Remove(tmp)
		return s, err
	}
	return s, os.Rename(tmp, c.Path)
}

// writeAll writes the content, sets the owner and then the mode (chown can
// clear mode bits), syncs and closes f.
func writeAll(f *os.File, c Change) error {
	_, err := f.Write(c.Content)
	if err == nil {
		err = f.Chown(c.UID, c.GID)
	}
	if err == nil {
		err = f.Chmod(c.Mode)
	}
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

func (s *staging) restore() error {
	if s.hadPrev {
		return os.Rename(s.path+".zimam-prev", s.path)
	}
	if err := os.Remove(s.path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

func (s *staging) commit() { _ = os.Remove(s.path + ".zimam-prev") }

// WithReloadLock runs fn holding the reload lock, so tests and reloads of the
// same service never overlap (§5.5). The locks directory must exist.
func WithReloadLock(ctx context.Context, p paths.Paths, fn func() error) error {
	unlock, err := sysexec.Lock(ctx, p.ReloadLock())
	if err != nil {
		return err
	}
	defer unlock()
	return fn()
}
