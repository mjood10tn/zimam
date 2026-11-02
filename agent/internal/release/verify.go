package release

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"time"

	"zimam/agent/contract"
)

// Manifest is manifest.json (design §3). The release tool writes it with
// this type, and the agent and the tool read it back with Verify.
type Manifest struct {
	Version         string            `json:"version"`
	Protocol        int               `json:"protocol"`
	MinAgentVersion string            `json:"min_agent_version"`
	PanelPHPMin     string            `json:"panel_php_min"`
	Expires         time.Time         `json:"expires"`
	Artifacts       map[string]string `json:"artifacts"`
}

var (
	ErrBadSignature = errors.New("the release manifest's signature is not valid")
	ErrExpired      = errors.New("the release manifest has expired")
)

// Verify checks the signature over the exact bytes before reading anything
// from them, then the versions, the expiry, the protocol and the minimum
// agent version.
func Verify(pub ed25519.PublicKey, manifest, sig []byte, now time.Time, agentVersion string) (*Manifest, error) {
	if len(pub) != ed25519.PublicKeySize || len(sig) != ed25519.SignatureSize || !ed25519.Verify(pub, manifest, sig) {
		return nil, ErrBadSignature
	}
	var m Manifest
	dec := json.NewDecoder(bytes.NewReader(manifest))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("release manifest: %w", err)
	}
	// An invalid min_agent_version would sort below every agent and let
	// each one through.
	if !ValidVersion(m.Version) {
		return nil, fmt.Errorf("release manifest: version %q is not a SemVer version", m.Version)
	}
	if !ValidVersion(m.MinAgentVersion) {
		return nil, fmt.Errorf("release manifest: min_agent_version %q is not a SemVer version", m.MinAgentVersion)
	}
	if !now.Before(m.Expires) {
		return nil, ErrExpired
	}
	if !slices.Contains(contract.SupportedProtocols, m.Protocol) {
		return nil, fmt.Errorf("release protocol %d is not supported by this agent", m.Protocol)
	}
	if CompareVersions(agentVersion, m.MinAgentVersion) < 0 {
		return nil, fmt.Errorf("the release needs agent %s or newer", m.MinAgentVersion)
	}
	return &m, nil
}

// CheckArtifact streams r and compares its SHA-256 with the manifest entry.
func (m *Manifest) CheckArtifact(name string, r io.Reader) error {
	want, ok := m.Artifacts[name]
	if !ok {
		return fmt.Errorf("artifact %s is not in the manifest", name)
	}
	h := sha256.New()
	if _, err := io.Copy(h, r); err != nil {
		return err
	}
	got := "sha256:" + hex.EncodeToString(h.Sum(nil))
	if subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
		return fmt.Errorf("artifact %s does not match the signed manifest", name)
	}
	return nil
}
