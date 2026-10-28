package php

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

// fixture reads a file from testdata. sury-apt.gpg is packages.sury.org's
// apt.gpg as downloaded on 2026-10-28; sury-apt.asc is the same key exported
// armored by gpg.
func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// otherKey is the fixture with one byte of its key material changed: still a
// well-formed v4 key, with another fingerprint.
func otherKey(t *testing.T) []byte {
	t.Helper()
	k := bytes.Clone(fixture(t, "sury-apt.gpg"))
	k[20] ^= 0xff
	return k
}

func TestFingerprintOfFixture(t *testing.T) {
	got, err := PrimaryFingerprint(fixture(t, "sury-apt.gpg"))
	if err != nil || got != SuryFingerprint {
		t.Errorf("PrimaryFingerprint = %q, %v; want %s", got, err, SuryFingerprint)
	}
	if got, err := PrimaryFingerprint(otherKey(t)); err != nil || got == SuryFingerprint {
		t.Errorf("a changed key gave %q, %v", got, err)
	}
}

func TestFingerprintArmored(t *testing.T) {
	armored := fixture(t, "sury-apt.asc")
	crlf := bytes.ReplaceAll(armored, []byte("\n"), []byte("\r\n"))
	for name, k := range map[string][]byte{"LF": armored, "CRLF": crlf} {
		got, err := PrimaryFingerprint(k)
		if err != nil || got != SuryFingerprint {
			t.Errorf("%s: PrimaryFingerprint = %q, %v; want %s", name, got, err, SuryFingerprint)
		}
	}
}

func TestFingerprintRefusesSecondKey(t *testing.T) {
	// APT trusts every key in a Signed-By keyring, so a key appended after
	// sury's must not pass on the strength of sury's fingerprint.
	two := append(bytes.Clone(fixture(t, "sury-apt.gpg")), otherKey(t)...)
	if got, err := PrimaryFingerprint(two); err == nil || !strings.Contains(err.Error(), "2 primary keys") {
		t.Errorf("PrimaryFingerprint = %q, %v; want a 2-primary-keys error", got, err)
	}
}

func TestFingerprintRefusesOtherVersions(t *testing.T) {
	k := bytes.Clone(fixture(t, "sury-apt.gpg"))
	// An old-format public key packet with a two-octet length: the version
	// is the first byte of the body.
	if k[0] != 0x99 || k[3] != 4 {
		t.Fatalf("the fixture starts % x, not a v4 public key with a two-octet length", k[:4])
	}
	for _, version := range []byte{3, 5, 6} {
		k[3] = version
		if got, err := PrimaryFingerprint(k); err == nil || !strings.Contains(err.Error(), "version") {
			t.Errorf("a v%d key gave %q, %v; want a version error", version, got, err)
		}
	}
}

// FuzzPrimaryFingerprint feeds the parser what a mirror could serve. It
// never panics, and an accepted keyring yields 40 upper-case hex digits.
func FuzzPrimaryFingerprint(f *testing.F) {
	k, err := os.ReadFile("testdata/sury-apt.gpg")
	if err != nil {
		f.Fatal(err)
	}
	armored, err := os.ReadFile("testdata/sury-apt.asc")
	if err != nil {
		f.Fatal(err)
	}
	// Besides the fixtures, a header cut short in each length form.
	for _, seed := range [][]byte{k, armored, k[:100], {0xc6, 0xe1, 0x04}, {0x99, 0x00, 0x01, 0x04},
		{0x98}, {0x99, 0x01}, {0x9a, 0x00, 0x00}, {0xc6}, {0xc6, 0xc0}, {0xc6, 0xff, 0x00}} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, keyring []byte) {
		fpr, err := PrimaryFingerprint(keyring)
		if err != nil {
			return
		}
		if len(fpr) != 40 || strings.Trim(fpr, "0123456789ABCDEF") != "" {
			t.Errorf("fingerprint %q isn't 40 upper-case hex digits", fpr)
		}
	})
}

func TestFingerprintRejectsMalformed(t *testing.T) {
	k := fixture(t, "sury-apt.gpg")
	armored := string(fixture(t, "sury-apt.asc"))
	for name, input := range map[string][]byte{
		"empty":           nil,
		"text":            []byte("not a key"),
		"truncated":       k[:100],
		"user ID first":   append([]byte{0xb4, 0x01, 'x'}, k...), // a one-byte user ID packet before the key
		"bad base64":      []byte(strings.Replace(armored, "mQGN", "m!GN", 1)),
		"armor no end":    []byte(strings.Split(armored, "-----END")[0]),
		"partial lengths": {0xc6, 0xe1, 0x04},
	} {
		if got, err := PrimaryFingerprint(input); err == nil {
			t.Errorf("%s: PrimaryFingerprint = %q, want an error", name, got)
		}
	}
}
