package configtx

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"
)

// Journal is the directory where Apply keeps a record of each change while
// it is in flight, locked by the process making it. If that process dies
// between staging and its test (a job's time limit, a reboot), the record
// outlives the lock, and Recover puts the previous files back. Empty turns
// records off; the agent sets it to Paths.ConfigTxDir() at start.
var Journal string

// entry is one path in a record, and whether a file was there before.
type entry struct {
	Path    string `json:"path"`
	HadPrev bool   `json:"had_prev"`
}

// record is one change's journal file, held under an exclusive flock until
// done.
type record struct {
	f    *os.File
	path string
}

// begin writes and locks the record for changes, before any is staged. It
// writes under a .tmp name and renames, so Recover never reads half a record.
func begin(changes []Change) (*record, error) {
	if Journal == "" {
		return nil, nil
	}
	entries := make([]entry, 0, len(changes))
	for _, c := range changes {
		_, err := os.Lstat(c.Path)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
		entries = append(entries, entry{Path: c.Path, HadPrev: err == nil})
	}
	data, err := json.Marshal(struct {
		Changes []entry `json:"changes"`
	}{entries})
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(Journal, 0o700); err != nil {
		return nil, err
	}
	b := make([]byte, 8)
	rand.Read(b)
	name := filepath.Join(Journal, hex.EncodeToString(b))
	f, err := os.OpenFile(name+".tmp", os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, err
	}
	err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	if err == nil {
		err = os.Rename(name+".tmp", name+".json")
	}
	if err != nil {
		f.Close()
		_ = os.Remove(name + ".tmp")
		return nil, fmt.Errorf("recording the change in %s: %w", Journal, err)
	}
	return &record{f: f, path: name + ".json"}, nil
}

// done removes the record and releases its lock: the change was kept or
// undone, either way finished.
func (r *record) done() {
	if r == nil {
		return
	}
	_ = os.Remove(r.path)
	r.f.Close()
}

// Recover undoes every change in dir whose process died before it finished,
// recognised by a record no process holds a lock on. It puts back each
// previous file, removes the files the change created, and returns the paths
// it touched. The previous files had passed their own test, so they're
// restored as they were. A change still in flight keeps its lock and is left
// alone.
func Recover(dir string) ([]string, error) {
	names, err := filepath.Glob(filepath.Join(dir, "*"))
	if err != nil {
		return nil, err
	}
	var touched []string
	var errs []error
	for _, name := range names {
		if strings.HasSuffix(name, ".tmp") {
			// begin renames a .tmp within moments; an old one is from a
			// process that died before staging anything.
			if fi, err := os.Lstat(name); err == nil && time.Since(fi.ModTime()) > time.Minute {
				_ = os.Remove(name)
			}
			continue
		}
		t, err := recoverRecord(name)
		touched = append(touched, t...)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", name, err))
		}
	}
	return touched, errors.Join(errs...)
}

func recoverRecord(name string) ([]string, error) {
	f, err := os.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return nil, nil // in flight
	}
	var rec struct {
		Changes []entry `json:"changes"`
	}
	if err := json.NewDecoder(f).Decode(&rec); err != nil {
		return nil, err
	}
	var touched []string
	for _, e := range slices.Backward(rec.Changes) {
		s := &staging{path: e.Path, hadPrev: e.HadPrev}
		if e.HadPrev {
			// No .zimam-prev means staging never reached this path, so the
			// file is the original.
			if _, err := os.Lstat(e.Path + ".zimam-prev"); err != nil {
				continue
			}
		} else if _, err := os.Lstat(e.Path); err != nil {
			continue
		}
		if err := s.restore(); err != nil {
			return touched, err
		}
		touched = append(touched, e.Path)
	}
	return touched, os.Remove(name)
}
