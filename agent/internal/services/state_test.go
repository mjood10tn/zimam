package services

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"zimam/agent/internal/paths"
)

func TestLoadWithoutFileIsNothingInstalled(t *testing.T) {
	p := paths.Paths{Root: t.TempDir()}
	st, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if st != (State{}) {
		t.Errorf("Load = %+v, want nothing installed", st)
	}
	// Reading creates nothing: a server without MySQL stays as it was.
	if _, err := os.Lstat(p.ServicesState()); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Load created %s (Lstat: %v)", p.ServicesState(), err)
	}
}

func TestServicesStateWritten(t *testing.T) {
	p := paths.Paths{Root: t.TempDir()}
	err := Update(p, func(s *State) { s.MySQL = Entry{Installed: true, Version: "8.4.13"} })
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	fi, err := os.Stat(p.ServicesState())
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("mode %v, want 0600", fi.Mode().Perm())
	}
	got, err := os.ReadFile(p.ServicesState())
	if err != nil {
		t.Fatal(err)
	}
	if want := "{\n  \"mysql\": {\n    \"installed\": true,\n    \"version\": \"8.4.13\"\n  }\n}\n"; string(got) != want {
		t.Errorf("file =\n%s\nwant\n%s", got, want)
	}
	st, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if st.MySQL != (Entry{Installed: true, Version: "8.4.13"}) || st.Valkey != (Entry{}) {
		t.Errorf("Load = %+v", st)
	}
	// No temporary file is left beside it.
	entries, err := os.ReadDir(filepath.Dir(p.ServicesState()))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("the state directory holds %d entries, want only services.json", len(entries))
	}
}

// Another service's entry survives an update of this one's.
func TestUpdateKeepsOtherEntries(t *testing.T) {
	p := paths.Paths{Root: t.TempDir()}
	if err := Update(p, func(s *State) { s.Valkey = Entry{Installed: true, Version: "9.0.4"} }); err != nil {
		t.Fatal(err)
	}
	if err := Update(p, func(s *State) { s.MySQL = Entry{Installed: true, Version: "8.4.13"} }); err != nil {
		t.Fatal(err)
	}
	st, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if st.MySQL.Version != "8.4.13" || st.Valkey.Version != "9.0.4" {
		t.Errorf("Load = %+v, want both entries", st)
	}
}

func TestLoadRefusesBadFiles(t *testing.T) {
	for name, content := range map[string]string{
		"corrupt":                     `{"mysql":`,
		"unknown service":             `{"redis":{"installed":true,"version":"1"}}`,
		"unknown field":               `{"mysql":{"installed":true,"version":"8.4.13","extra":1}}`,
		"installed without version":   `{"mysql":{"installed":true,"version":""}}`,
		"version while not installed": `{"mysql":{"installed":false,"version":"8.4.13"}}`,
		"version with a newline":      `{"mysql":{"installed":true,"version":"8.4\n"}}`,
	} {
		p := paths.Paths{Root: t.TempDir()}
		if err := os.MkdirAll(p.StateDir(), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p.ServicesState(), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(p); err == nil || !strings.Contains(err.Error(), "services.json") {
			t.Errorf("%s: Load = %v, want an error naming the file", name, err)
		}
	}
}

func TestUpdateRefusesAnInvalidEntry(t *testing.T) {
	p := paths.Paths{Root: t.TempDir()}
	if err := Update(p, func(s *State) { s.MySQL = Entry{Installed: true} }); err == nil {
		t.Error("Update accepted an installed service without a version")
	}
	if _, err := os.Lstat(p.ServicesState()); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("a refused update wrote the file (Lstat: %v)", err)
	}
}
