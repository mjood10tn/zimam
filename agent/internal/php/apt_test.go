package php

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"zimam/agent/internal/configtx"
	"zimam/agent/internal/sysexec"
)

func TestEnsureRepoRejectsWrongKey(t *testing.T) {
	e := newEnv(t, otherKey(t), "/php/")
	e.run.Add(aptUpdate)
	err := EnsureRepo(context.Background(), e.d)
	if err == nil {
		t.Fatal("EnsureRepo accepted a key with another fingerprint")
	}
	other, ferr := PrimaryFingerprint(otherKey(t))
	if ferr != nil {
		t.Fatal(ferr)
	}
	for _, fpr := range []string{other, SuryFingerprint} {
		if !strings.Contains(err.Error(), fpr) {
			t.Errorf("the error doesn't name %s: %v", fpr, err)
		}
	}
	for _, path := range []string{e.p.SuryKeyring(), e.p.SurySources(), e.p.SuryPin()} {
		if _, err := os.Lstat(path); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("%s was written (Lstat: %v)", path, err)
		}
	}
	if calls := e.run.Calls(); len(calls) != 0 {
		t.Errorf("ran %q", commands(calls))
	}
}

func TestEnsureRepoMirrorPinHost(t *testing.T) {
	e := newEnv(t, fixture(t, "sury-apt.gpg"), "/mirror/sury/php/")
	e.run.Add(aptUpdate)
	if err := EnsureRepo(context.Background(), e.d); err != nil {
		t.Fatalf("EnsureRepo: %v", err)
	}
	pin := readFile(t, e.p.SuryPin())
	if !strings.Contains(pin, "Pin: origin \"127.0.0.1\"\n") || strings.Contains(pin, "packages.sury.org") {
		t.Errorf("the pin doesn't name the mirror's host:\n%s", pin)
	}
	src := readFile(t, e.p.SurySources())
	for _, want := range []string{"URIs: " + e.d.Config.PHPRepoURL + "\n", "Suites: resolute\n", "Signed-By: /etc/apt/keyrings/zimam-sury.gpg\n"} {
		if !strings.Contains(src, want) {
			t.Errorf("the source lacks %q:\n%s", want, src)
		}
	}
	if got := readFile(t, e.p.SuryKeyring()); got != string(fixture(t, "sury-apt.gpg")) {
		t.Error("the keyring written isn't the one downloaded")
	}
	for _, path := range []string{e.p.SuryKeyring(), e.p.SurySources(), e.p.SuryPin()} {
		if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o644 {
			t.Errorf("%s: mode %v, %v; want 0644", path, fi.Mode().Perm(), err)
		}
	}
	calls := e.run.Calls()
	if want := []string{line(aptUpdate)}; !slices.Equal(commands(calls), want) {
		t.Errorf("ran %q, want %q", commands(calls), want)
	}
	if len(calls) > 0 && !slices.Contains(calls[0].Env, "DEBIAN_FRONTEND=noninteractive") {
		t.Errorf("apt-get ran with env %q", calls[0].Env)
	}
}

func TestEnsureRepoWritesArmoredKeyringAsBinary(t *testing.T) {
	// APT reads a .gpg keyring only in binary form.
	e := newEnv(t, fixture(t, "sury-apt.asc"), "/php/")
	e.run.Add(aptUpdate)
	if err := EnsureRepo(context.Background(), e.d); err != nil {
		t.Fatalf("EnsureRepo: %v", err)
	}
	k, err := os.ReadFile(e.p.SuryKeyring())
	if err != nil {
		t.Fatal(err)
	}
	if bytes.HasPrefix(k, []byte("-----BEGIN")) {
		t.Fatal("the keyring was written armored")
	}
	if fpr, err := PrimaryFingerprint(k); err != nil || fpr != SuryFingerprint {
		t.Errorf("the written keyring has %q, %v", fpr, err)
	}
}

func TestEnsureRepoRefusesHugeDownload(t *testing.T) {
	e := newEnv(t, bytes.Repeat([]byte{0x99}, 1<<20+1), "/php/")
	err := EnsureRepo(context.Background(), e.d)
	if err == nil || !strings.Contains(err.Error(), "larger than") {
		t.Errorf("EnsureRepo = %v, want a too-large error", err)
	}
}

func TestEnsureRepoRestoresOnUpdateFailure(t *testing.T) {
	e := newEnv(t, fixture(t, "sury-apt.gpg"), "/php/")
	for _, path := range []string{e.p.SuryKeyring(), e.p.SurySources(), e.p.SuryPin()} {
		mkdir(t, filepath.Dir(path))
		writeFile(t, path, "previous\n")
	}
	failing := aptUpdate
	failing.Result = sysexec.Result{ExitCode: 100, Stderr: []byte("E: The repository 'https://127.0.0.1/php resolute InRelease' is not signed.\n")}
	e.run.Add(failing)
	e.run.Add(aptUpdate) // the retest, with the previous files back
	err := EnsureRepo(context.Background(), e.d)
	var failed *configtx.TestFailed
	if !errors.As(err, &failed) || !strings.Contains(err.Error(), "is not signed") {
		t.Fatalf("EnsureRepo = %v, want a test failure with apt-get's output", err)
	}
	for _, path := range []string{e.p.SuryKeyring(), e.p.SurySources(), e.p.SuryPin()} {
		if got := readFile(t, path); got != "previous\n" {
			t.Errorf("%s = %q, want the previous file back", path, got)
		}
	}
}
