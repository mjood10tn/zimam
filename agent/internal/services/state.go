// Package services records which optional services Zimam installed on this
// server, MySQL and Valkey, in /var/lib/zimam/services.json (root 0600).
// A server that never ran an install has no file, and that reads as nothing
// installed.
package services

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"regexp"

	"zimam/agent/contract"
	"zimam/agent/internal/paths"
)

// Entry is one service: whether Zimam installed it and the version its
// package reports. An entry that isn't installed has no version.
type Entry struct {
	Installed bool   `json:"installed"`
	Version   string `json:"version"`
}

// State is the file's content. A service without an entry is not installed.
// A new service adds a field here and nothing else changes.
type State struct {
	MySQL  Entry `json:"mysql,omitzero"`
	Valkey Entry `json:"valkey,omitzero"`
}

// versionRe is what an upstream package version may hold, so a version can
// be shown and compared without surprises.
var versionRe = regexp.MustCompile(`^[0-9][0-9A-Za-z.+~-]{0,63}$`)

func (e Entry) validate() error {
	switch {
	case !e.Installed && e.Version != "":
		return errors.New("has a version but isn't installed")
	case e.Installed && !versionRe.MatchString(e.Version):
		return fmt.Errorf("version %q isn't a package version", e.Version)
	}
	return nil
}

func (s State) validate() error {
	if err := s.MySQL.validate(); err != nil {
		return fmt.Errorf("mysql %v", err)
	}
	if err := s.Valkey.validate(); err != nil {
		return fmt.Errorf("valkey %v", err)
	}
	return nil
}

// Load reads the file strictly. A missing file is nothing installed.
func Load(p paths.Paths) (State, error) {
	data, err := os.ReadFile(p.ServicesState())
	if errors.Is(err, fs.ErrNotExist) {
		return State{}, nil
	}
	if err != nil {
		return State{}, err
	}
	var st State
	if err := contract.DecodeStrict(data, &st); err != nil {
		return State{}, fmt.Errorf("%s: %w", p.ServicesState(), err)
	}
	if err := st.validate(); err != nil {
		return State{}, fmt.Errorf("%s: %w", p.ServicesState(), err)
	}
	return st, nil
}

// Update reads the file, lets change edit it and writes the result
// atomically, root 0600. It touches only what change touches, so one
// service's install leaves the other's entry as it was. Installs run under
// the server subject's lock, so two never overlap.
func Update(p paths.Paths, change func(*State)) error {
	st, err := Load(p)
	if err != nil {
		return err
	}
	change(&st)
	if err := st.validate(); err != nil {
		return fmt.Errorf("%s: %w", p.ServicesState(), err)
	}
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	dir := p.StateDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".services-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // fails harmlessly once renamed
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), p.ServicesState())
}
