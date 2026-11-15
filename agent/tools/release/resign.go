package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"zimam/agent/internal/release"
)

// maxResignDays keeps a re-signed manifest to the 30 days a build gives it
// (design §3).
const maxResignDays = 30

// resign gives a published release's manifest a new expiry, -days from now,
// and signs it again (R01-D18). The directory must verify as it is, except
// for the expiry: the signature under this key, every artifact's hash and
// the file set. The new manifest differs from the old one only in the
// expires value, and is verified again once written.
func resign(args []string, stdout, stderr io.Writer) error {
	f := flags("resign", stderr)
	key := f.String("key", "", "")
	days := f.Int("days", 0, "")
	if f.Parse(args) != nil || *key == "" || *days < 1 || *days > maxResignDays || f.NArg() != 1 {
		return errUsage
	}
	priv, err := readPrivateKey(*key)
	if err != nil {
		return err
	}
	pub := priv.Public().(ed25519.PublicKey)
	dir := f.Arg(0)
	manifestPath := filepath.Join(dir, "manifest.json")
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return err
	}
	sig, err := os.ReadFile(manifestPath + ".sig")
	if err != nil {
		return err
	}
	m, err := verifyIgnoringExpiry(pub, data, sig)
	if err != nil {
		return err
	}
	if err := checkDir(m, dir); err != nil {
		return err
	}
	// Re-encoding must give back the exact bytes, so that setting expires
	// and encoding again changes nothing else.
	if again, err := encodeManifest(m); err != nil {
		return err
	} else if !bytes.Equal(again, data) {
		return errors.New("manifest.json isn't in the form the release tool writes, so it can't be re-signed with only its expiry changed")
	}
	old := m.Expires
	m.Expires = time.Now().UTC().Add(time.Duration(*days) * 24 * time.Hour).Truncate(time.Second)
	newData, err := encodeManifest(m)
	if err != nil {
		return err
	}
	if err := replaceFile(manifestPath, newData); err != nil {
		return err
	}
	if err := replaceFile(manifestPath+".sig", ed25519.Sign(priv, newData)); err != nil {
		return err
	}
	// The check verify makes, at the real time, on what was written.
	if data, err = os.ReadFile(manifestPath); err != nil {
		return err
	}
	if sig, err = os.ReadFile(manifestPath + ".sig"); err != nil {
		return err
	}
	if m, err = release.Verify(pub, data, sig, time.Now(), m.Version); err != nil {
		return fmt.Errorf("the re-signed manifest: %w", err)
	}
	if err := checkDir(m, dir); err != nil {
		return fmt.Errorf("the re-signed manifest: %w", err)
	}
	fmt.Fprintf(stdout, "release %s re-signed: expires %s, was %s\n",
		m.Version, m.Expires.Format(time.RFC3339), old.Format(time.RFC3339))
	return nil
}

// verifyIgnoringExpiry runs release.Verify at a moment just before the
// manifest expires, so every check but the expiry applies. The signature is
// checked before the expiry is read from the bytes, and release.Verify
// checks it again. The agent version is the release's own, which is its
// min_agent_version.
func verifyIgnoringExpiry(pub ed25519.PublicKey, data, sig []byte) (*release.Manifest, error) {
	if len(sig) != ed25519.SignatureSize || !ed25519.Verify(pub, data, sig) {
		return nil, release.ErrBadSignature
	}
	var head struct {
		Version string    `json:"version"`
		Expires time.Time `json:"expires"`
	}
	if err := json.Unmarshal(data, &head); err != nil {
		return nil, fmt.Errorf("release manifest: %w", err)
	}
	return release.Verify(pub, data, sig, head.Expires.Add(-time.Second), head.Version)
}

// replaceFile writes data to a new file beside name, then renames it over
// name, so name never holds a partial write.
func replaceFile(name string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(name), "."+filepath.Base(name)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), name)
}
