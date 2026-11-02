package release

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

var testNow = time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)

func testKey(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	return pub, priv
}

func testManifest() Manifest {
	return Manifest{
		Version:         "0.1.0",
		Protocol:        1,
		MinAgentVersion: "0.1.0",
		PanelPHPMin:     "8.3",
		Expires:         testNow.Add(24 * time.Hour),
		Artifacts:       map[string]string{"install.sh": sha256Of("#!/bin/bash\n")},
	}
}

func sha256Of(s string) string {
	sum := sha256.Sum256([]byte(s))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// signed encodes m and signs the exact bytes.
func signed(t *testing.T, priv ed25519.PrivateKey, m Manifest) (manifest, sig []byte) {
	t.Helper()
	manifest, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return manifest, ed25519.Sign(priv, manifest)
}

func TestVerifyAccepts(t *testing.T) {
	pub, priv := testKey(t)
	manifest, sig := signed(t, priv, testManifest())
	m, err := Verify(pub, manifest, sig, testNow, "0.1.0")
	if err != nil {
		t.Fatal(err)
	}
	if m.Version != "0.1.0" || m.PanelPHPMin != "8.3" || m.Artifacts["install.sh"] == "" {
		t.Fatalf("manifest read as %+v", m)
	}
}

func TestVerifyRejectsOneFlippedByte(t *testing.T) {
	pub, priv := testKey(t)
	manifest, sig := signed(t, priv, testManifest())
	for i := range manifest {
		tampered := []byte(string(manifest))
		tampered[i] ^= 0x01
		if _, err := Verify(pub, tampered, sig, testNow, "0.1.0"); !errors.Is(err, ErrBadSignature) {
			t.Fatalf("byte %d flipped: err = %v, want ErrBadSignature", i, err)
		}
	}
	for i := range sig {
		tampered := []byte(string(sig))
		tampered[i] ^= 0x01
		if _, err := Verify(pub, manifest, tampered, testNow, "0.1.0"); !errors.Is(err, ErrBadSignature) {
			t.Fatalf("signature byte %d flipped: err = %v, want ErrBadSignature", i, err)
		}
	}
}

func TestVerifyRejectsWrongKey(t *testing.T) {
	_, priv := testKey(t)
	other, _ := testKey(t)
	manifest, sig := signed(t, priv, testManifest())
	if _, err := Verify(other, manifest, sig, testNow, "0.1.0"); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("err = %v, want ErrBadSignature", err)
	}
	if _, err := Verify(nil, manifest, sig, testNow, "0.1.0"); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("no key: err = %v, want ErrBadSignature", err)
	}
}

func TestVerifyRejectsShortSignature(t *testing.T) {
	pub, priv := testKey(t)
	manifest, sig := signed(t, priv, testManifest())
	for _, s := range [][]byte{nil, sig[:ed25519.SignatureSize-1], append(sig, 0)} {
		if _, err := Verify(pub, manifest, s, testNow, "0.1.0"); !errors.Is(err, ErrBadSignature) {
			t.Fatalf("%d-byte signature: err = %v, want ErrBadSignature", len(s), err)
		}
	}
}

func TestVerifyChecksSignatureBeforeParsing(t *testing.T) {
	pub, priv := testKey(t)
	garbage := []byte(`{"version": not json`)
	if _, err := Verify(pub, garbage, make([]byte, ed25519.SignatureSize), testNow, "0.1.0"); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("unsigned garbage: err = %v, want ErrBadSignature", err)
	}
	// Signed garbage gets past the signature and fails on parsing.
	_, err := Verify(pub, garbage, ed25519.Sign(priv, garbage), testNow, "0.1.0")
	if err == nil || errors.Is(err, ErrBadSignature) {
		t.Fatalf("signed garbage: err = %v, want a parse error", err)
	}
}

func TestVerifyRejectsUnknownField(t *testing.T) {
	pub, priv := testKey(t)
	manifest, _ := signed(t, priv, testManifest())
	manifest = []byte(strings.Replace(string(manifest), `{`, `{"extra":1,`, 1))
	_, err := Verify(pub, manifest, ed25519.Sign(priv, manifest), testNow, "0.1.0")
	if err == nil || !strings.Contains(err.Error(), "extra") {
		t.Fatalf("err = %v, want an unknown field error", err)
	}
}

func TestVerifyRejectsExpired(t *testing.T) {
	pub, priv := testKey(t)
	m := testManifest()
	manifest, sig := signed(t, priv, m)
	for _, now := range []time.Time{m.Expires, m.Expires.Add(time.Second)} {
		if _, err := Verify(pub, manifest, sig, now, "0.1.0"); !errors.Is(err, ErrExpired) {
			t.Fatalf("at %s: err = %v, want ErrExpired", now, err)
		}
	}
	if _, err := Verify(pub, manifest, sig, m.Expires.Add(-time.Second), "0.1.0"); err != nil {
		t.Fatalf("a second before expiry: %v", err)
	}
}

func TestVerifyRejectsUnknownProtocol(t *testing.T) {
	pub, priv := testKey(t)
	for _, p := range []int{0, 2} {
		m := testManifest()
		m.Protocol = p
		manifest, sig := signed(t, priv, m)
		_, err := Verify(pub, manifest, sig, testNow, "0.1.0")
		if err == nil || !strings.Contains(err.Error(), "protocol") {
			t.Fatalf("protocol %d: err = %v, want a protocol error", p, err)
		}
	}
}

func TestVerifyRejectsOldAgent(t *testing.T) {
	pub, priv := testKey(t)
	manifest, sig := signed(t, priv, testManifest())
	for _, agent := range []string{"0.0.9", "0.1.0-rc.1", "0.0.0-dev", "not-a-version"} {
		_, err := Verify(pub, manifest, sig, testNow, agent)
		if err == nil || !strings.Contains(err.Error(), "0.1.0 or newer") {
			t.Fatalf("agent %s: err = %v, want a minimum version error", agent, err)
		}
	}
	for _, agent := range []string{"0.1.0", "0.1.1", "1.0.0"} {
		if _, err := Verify(pub, manifest, sig, testNow, agent); err != nil {
			t.Fatalf("agent %s: %v", agent, err)
		}
	}
}

// A signed manifest whose versions aren't SemVer is refused: an invalid
// minimum would otherwise let every agent through.
func TestVerifyRejectsBadVersions(t *testing.T) {
	pub, priv := testKey(t)
	for _, bad := range []string{"", "v0.1.0", "0.1", "latest", "../0.1.0"} {
		for _, field := range []string{"version", "min_agent_version"} {
			m := testManifest()
			if field == "version" {
				m.Version = bad
			} else {
				m.MinAgentVersion = bad
			}
			manifest, sig := signed(t, priv, m)
			_, err := Verify(pub, manifest, sig, testNow, "0.1.0")
			if err == nil || !strings.Contains(err.Error(), field) {
				t.Fatalf("%s %q: err = %v, want an error naming the field", field, bad, err)
			}
		}
	}
}

func TestCheckArtifactMismatch(t *testing.T) {
	m := testManifest()
	if err := m.CheckArtifact("install.sh", strings.NewReader("#!/bin/bash\n")); err != nil {
		t.Fatalf("matching artifact: %v", err)
	}
	err := m.CheckArtifact("install.sh", strings.NewReader("#!/bin/sh\n"))
	if err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("err = %v, want a mismatch", err)
	}
}

func TestCheckArtifactMissing(t *testing.T) {
	m := testManifest()
	err := m.CheckArtifact("zimam-agent-linux-amd64", strings.NewReader(""))
	if err == nil || !strings.Contains(err.Error(), "not in the manifest") {
		t.Fatalf("err = %v, want a missing artifact error", err)
	}
}

func TestCompareVersions(t *testing.T) {
	// Each list is in ascending SemVer precedence (semver.org, item 11).
	ascending := []string{
		"0.0.0-dev",
		"0.0.0",
		"0.1.0-alpha",
		"0.1.0-alpha.1",
		"0.1.0-alpha.beta",
		"0.1.0-beta",
		"0.1.0-beta.2",
		"0.1.0-beta.11",
		"0.1.0-rc.1",
		"0.1.0",
		"0.9.0",
		"0.10.0",
		"1.0.0",
		"1.9.9",
		"1.10.0",
		"99999999999999999999.0.0",
	}
	for i, a := range ascending {
		for j, b := range ascending {
			want := 0
			if i < j {
				want = -1
			} else if i > j {
				want = 1
			}
			if got := CompareVersions(a, b); got != want {
				t.Errorf("CompareVersions(%q, %q) = %d, want %d", a, b, got, want)
			}
		}
	}
	if got := CompareVersions("1.0.0+build.1", "1.0.0+build.2"); got != 0 {
		t.Errorf("build metadata compared: %d", got)
	}
	// A string that isn't SemVer sorts before every version.
	for _, bad := range []string{"", "v1.0.0", "1.0", "01.0.0", "1.0.0-", "1.0.0-01"} {
		if ValidVersion(bad) {
			t.Errorf("ValidVersion(%q) = true", bad)
		}
		if got := CompareVersions(bad, "0.0.0-dev"); got != -1 {
			t.Errorf("CompareVersions(%q, 0.0.0-dev) = %d, want -1", bad, got)
		}
		if got := CompareVersions("0.0.0-dev", bad); got != 1 {
			t.Errorf("CompareVersions(0.0.0-dev, %q) = %d, want 1", bad, got)
		}
	}
}
