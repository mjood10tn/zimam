package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"zimam/agent/internal/release"
)

// runTool runs the tool's command line in-process.
func runTool(t *testing.T, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	var out, errOut bytes.Buffer
	code = run(args, &out, &errOut)
	return out.String(), errOut.String(), code
}

// mustRun fails the test unless the command exits 0.
func mustRun(t *testing.T, args ...string) string {
	t.Helper()
	stdout, stderr, code := runTool(t, args...)
	if code != 0 {
		t.Fatalf("release %s: exit %d: %s", strings.Join(args, " "), code, stderr)
	}
	return stdout
}

// keyPair runs keygen into a new directory and returns the two key files.
func keyPair(t *testing.T) (private, public string) {
	t.Helper()
	dir := t.TempDir()
	mustRun(t, "keygen", "-out", dir)
	return filepath.Join(dir, "release-key"), filepath.Join(dir, "release-key.pub")
}

func writeFile(t *testing.T, name, content string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return name
}

// composerFiles writes a composer.json whose config.platform.php is json
// and a composer.lock resolved for lock, and returns composer.json's path.
func composerFiles(t *testing.T, json, lock string) string {
	t.Helper()
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "composer.lock"), `{"platform-overrides": {"php": "`+lock+`"}}`)
	return writeFile(t, filepath.Join(dir, "composer.json"), `{"config": {"platform": {"php": "`+json+`"}}}`)
}

func sha256Hex(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// dist builds a signed release directory with two artifacts and returns it
// with the public key file.
func dist(t *testing.T) (dir, public string) {
	t.Helper()
	private, public := keyPair(t)
	dir = t.TempDir()
	writeFile(t, filepath.Join(dir, "zimam-agent-linux-amd64"), "agent")
	writeFile(t, filepath.Join(dir, "install.sh"), "#!/bin/bash\n")
	mustRun(t, "manifest", "-version", "0.1.0", "-expires", time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
		"-composer", composerFiles(t, "8.3", "8.3"), "-out", filepath.Join(dir, "manifest.json"),
		filepath.Join(dir, "zimam-agent-linux-amd64"), filepath.Join(dir, "install.sh"))
	mustRun(t, "sign", "-key", private, filepath.Join(dir, "manifest.json"))
	return dir, public
}

func TestKeygenWritesKeyPair(t *testing.T) {
	private, public := keyPair(t)
	fi, err := os.Stat(private)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("private key mode %o, want 600", fi.Mode().Perm())
	}
	privPEM, _ := os.ReadFile(private)
	block, _ := pem.Decode(privPEM)
	if block == nil || block.Type != "PRIVATE KEY" {
		t.Fatalf("private key isn't a PKCS #8 PEM block: %q", privPEM)
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	priv, ok := key.(ed25519.PrivateKey)
	if !ok {
		t.Fatalf("private key is %T", key)
	}
	pubPEM, _ := os.ReadFile(public)
	block, _ = pem.Decode(pubPEM)
	if block == nil || block.Type != "PUBLIC KEY" {
		t.Fatalf("public key isn't a PEM public key: %q", pubPEM)
	}
	pub, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if !priv.Public().(ed25519.PublicKey).Equal(pub) {
		t.Fatal("the public key doesn't belong to the private key")
	}
}

func TestKeygenRefusesToOverwrite(t *testing.T) {
	private, public := keyPair(t)
	before, _ := os.ReadFile(private)
	for _, existing := range []string{private, public} {
		dir := t.TempDir()
		writeFile(t, filepath.Join(dir, filepath.Base(existing)), "keep me")
		if _, _, code := runTool(t, "keygen", "-out", dir); code == 0 {
			t.Fatalf("keygen wrote over an existing %s", filepath.Base(existing))
		}
		if got, _ := os.ReadFile(filepath.Join(dir, filepath.Base(existing))); string(got) != "keep me" {
			t.Fatalf("%s was overwritten", filepath.Base(existing))
		}
	}
	if _, _, code := runTool(t, "keygen", "-out", filepath.Dir(private)); code == 0 {
		t.Fatal("keygen ran twice into one directory")
	}
	if after, _ := os.ReadFile(private); !bytes.Equal(before, after) {
		t.Fatal("the first private key changed")
	}
}

func TestPubkeyPrintsBothForms(t *testing.T) {
	private, public := keyPair(t)
	pubPEM, _ := os.ReadFile(public)
	if got := mustRun(t, "pubkey", "-key", private); got != string(pubPEM) {
		t.Fatalf("pubkey printed %q, want %q", got, pubPEM)
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(mustRun(t, "pubkey", "-key", private, "-raw")))
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(pubPEM)
	pub, _ := x509.ParsePKIXPublicKey(block.Bytes)
	if !bytes.Equal(raw, pub.(ed25519.PublicKey)) {
		t.Fatal("-raw isn't the 32 raw key bytes")
	}
}

func TestManifestDeterministic(t *testing.T) {
	dir := t.TempDir()
	agent := writeFile(t, filepath.Join(dir, "zimam-agent-linux-amd64"), "agent")
	installer := writeFile(t, filepath.Join(dir, "install.sh"), "#!/bin/bash\n")
	composer := composerFiles(t, "8.3", "8.3")
	build := func(out string, artifacts ...string) []byte {
		args := append([]string{"manifest", "-version", "0.1.0", "-expires", "2026-12-02T00:00:00Z",
			"-composer", composer, "-out", out}, artifacts...)
		mustRun(t, args...)
		data, err := os.ReadFile(out)
		if err != nil {
			t.Fatal(err)
		}
		return data
	}
	first := build(filepath.Join(t.TempDir(), "manifest.json"), agent, installer)
	second := build(filepath.Join(t.TempDir(), "manifest.json"), installer, agent)
	if !bytes.Equal(first, second) {
		t.Fatalf("two builds differ:\n%s\n%s", first, second)
	}
	// The expiry is written in UTC, in whole seconds, however it was given.
	args := []string{"manifest", "-version", "0.1.0", "-expires", "2026-12-02T03:00:00.25+03:00", "-composer", composer}
	third := filepath.Join(t.TempDir(), "manifest.json")
	mustRun(t, append(args, "-out", third, agent, installer)...)
	if data, _ := os.ReadFile(third); !bytes.Equal(first, data) {
		t.Fatalf("an equal expiry in another zone gives:\n%s", data)
	}
	if !bytes.HasSuffix(first, []byte("}\n")) || !bytes.Contains(first, []byte(`"expires": "2026-12-02T00:00:00Z"`)) {
		t.Fatalf("unexpected encoding:\n%s", first)
	}
	// The manifest the tool writes is the one the agent reads.
	_, priv, _ := ed25519.GenerateKey(nil)
	m, err := release.Verify(priv.Public().(ed25519.PublicKey), first, ed25519.Sign(priv, first),
		time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC), "0.1.0")
	if err != nil {
		t.Fatal(err)
	}
	want := release.Manifest{
		Version:         "0.1.0",
		Protocol:        1,
		MinAgentVersion: "0.1.0",
		PanelPHPMin:     "8.3",
		Expires:         time.Date(2026, 12, 2, 0, 0, 0, 0, time.UTC),
		Artifacts: map[string]string{
			"zimam-agent-linux-amd64": sha256Hex(t, agent),
			"install.sh":              sha256Hex(t, installer),
		},
	}
	got, _ := json.Marshal(m)
	wantJSON, _ := json.Marshal(want)
	if !bytes.Equal(got, wantJSON) {
		t.Fatalf("manifest = %s\nwant %s", got, wantJSON)
	}
}

// panel_php_min is Composer's config.platform.php, and composer.lock must
// have been resolved for it, so the bundled vendor/ runs on that PHP (D7).
func TestManifestPanelPHPFromComposer(t *testing.T) {
	artifact := writeFile(t, filepath.Join(t.TempDir(), "install.sh"), "x")
	manifest := func(composer string) (string, int) {
		out := filepath.Join(t.TempDir(), "manifest.json")
		_, stderr, code := runTool(t, "manifest", "-version", "0.1.0", "-expires", "2026-12-02T00:00:00Z",
			"-composer", composer, "-out", out, artifact)
		data, _ := os.ReadFile(out)
		return string(data) + stderr, code
	}
	if out, code := manifest(composerFiles(t, "8.4", "8.4")); code != 0 || !strings.Contains(out, `"panel_php_min": "8.4"`) {
		t.Fatalf("8.4: exit %d: %s", code, out)
	}
	if out, code := manifest(composerFiles(t, "8.4", "8.3")); code == 0 || !strings.Contains(out, "composer.lock") {
		t.Fatalf("lock resolved for another PHP: exit %d: %s", code, out)
	}
	for _, bad := range []string{"", "8", "8.3.0", "^8.3"} {
		if out, code := manifest(composerFiles(t, bad, bad)); code == 0 || !strings.Contains(out, "config.platform.php") {
			t.Fatalf("platform %q: exit %d: %s", bad, code, out)
		}
	}
}

func TestManifestRefusesBadInput(t *testing.T) {
	dir := t.TempDir()
	artifact := writeFile(t, filepath.Join(dir, "install.sh"), "x")
	same := writeFile(t, filepath.Join(dir, "other", "install.sh"), "y")
	composer := composerFiles(t, "8.3", "8.3")
	// A manifest from an earlier build is there, as in a reused dist/.
	out := writeFile(t, filepath.Join(dir, "manifest.json"), "{}")
	for name, args := range map[string][]string{
		"tag as version":    {"-version", "v0.1.0", "-expires", "2026-12-02T00:00:00Z", artifact},
		"unparsed expiry":   {"-version", "0.1.0", "-expires", "in 30 days", artifact},
		"no artifacts":      {"-version", "0.1.0", "-expires", "2026-12-02T00:00:00Z"},
		"duplicate name":    {"-version", "0.1.0", "-expires", "2026-12-02T00:00:00Z", artifact, same},
		"missing artifact":  {"-version", "0.1.0", "-expires", "2026-12-02T00:00:00Z", filepath.Join(dir, "nope")},
		"the manifest self": {"-version", "0.1.0", "-expires", "2026-12-02T00:00:00Z", out},
	} {
		args = append([]string{"manifest", "-composer", composer, "-out", out}, args...)
		if _, _, code := runTool(t, args...); code == 0 {
			t.Errorf("%s: manifest exited 0", name)
		}
	}
}

func TestToolSignVerifiesWithOpenSSL(t *testing.T) {
	if _, err := exec.LookPath("openssl"); err != nil {
		t.Skip("no openssl")
	}
	private, public := keyPair(t)
	dir := t.TempDir()
	manifest := writeFile(t, filepath.Join(dir, "manifest.json"), `{"version": "0.1.0"}`+"\n")
	mustRun(t, "sign", "-key", private, manifest)
	sig, err := os.ReadFile(manifest + ".sig")
	if err != nil {
		t.Fatal(err)
	}
	if len(sig) != ed25519.SignatureSize {
		t.Fatalf("signature is %d bytes, want raw %d", len(sig), ed25519.SignatureSize)
	}
	// The exact command install.sh runs (design §3).
	opensslVerify := func() error {
		cmd := exec.Command("openssl", "pkeyutl", "-verify", "-pubin", "-inkey", public, "-rawin",
			"-in", "manifest.json", "-sigfile", "manifest.json.sig")
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Logf("openssl: %s", out)
		}
		return err
	}
	if err := opensslVerify(); err != nil {
		t.Fatalf("openssl rejected the tool's signature: %v", err)
	}
	writeFile(t, manifest, `{"version": "0.1.1"}`+"\n")
	if opensslVerify() == nil {
		t.Fatal("openssl accepted a changed manifest")
	}
}

func TestVerifyCommand(t *testing.T) {
	dir, public := dist(t)
	if out := mustRun(t, "verify", "-key", public, "-agent-version", "0.1.0", dir); !strings.Contains(out, "0.1.0") {
		t.Fatalf("verify printed %q", out)
	}
	_, otherPublic := keyPair(t)
	if _, _, code := runTool(t, "verify", "-key", otherPublic, "-agent-version", "0.1.0", dir); code == 0 {
		t.Fatal("verify accepted another key")
	}
	if _, _, code := runTool(t, "verify", "-key", public, "-agent-version", "0.0.9", dir); code == 0 {
		t.Fatal("verify accepted an agent older than min_agent_version")
	}
	for name, tamper := range map[string]func(dir string){
		"changed artifact": func(dir string) { writeFile(t, filepath.Join(dir, "install.sh"), "#!/bin/sh\n") },
		"missing artifact": func(dir string) { os.Remove(filepath.Join(dir, "zimam-agent-linux-amd64")) },
		"changed manifest": func(dir string) {
			name := filepath.Join(dir, "manifest.json")
			data, _ := os.ReadFile(name)
			writeFile(t, name, strings.Replace(string(data), "0.1.0", "0.1.1", 1))
		},
		"missing signature": func(dir string) { os.Remove(filepath.Join(dir, "manifest.json.sig")) },
		// publish uploads every file in the directory, so one the manifest
		// doesn't list would ship unsigned.
		"extra file":      func(dir string) { writeFile(t, filepath.Join(dir, "install-extra.sh"), "#!/bin/sh\n") },
		"extra directory": func(dir string) { os.Mkdir(filepath.Join(dir, "sub"), 0o755) },
	} {
		dir, public := dist(t)
		tamper(dir)
		if _, _, code := runTool(t, "verify", "-key", public, "-agent-version", "0.1.0", dir); code == 0 {
			t.Errorf("%s: verify exited 0", name)
		}
	}
}

// A signed manifest still never names a file outside the release directory.
func TestVerifyRefusesArtifactPaths(t *testing.T) {
	private, public := keyPair(t)
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "x"), "x")
	for _, name := range []string{"../x", "sub/x", ".", ""} {
		m, _ := json.Marshal(release.Manifest{
			Version: "0.1.0", Protocol: 1, MinAgentVersion: "0.1.0", PanelPHPMin: "8.3",
			Expires:   time.Now().Add(time.Hour).UTC().Truncate(time.Second),
			Artifacts: map[string]string{name: sha256Hex(t, filepath.Join(dir, "x"))},
		})
		writeFile(t, filepath.Join(dir, "manifest.json"), string(m))
		mustRun(t, "sign", "-key", private, filepath.Join(dir, "manifest.json"))
		_, stderr, code := runTool(t, "verify", "-key", public, "-agent-version", "0.1.0", dir)
		if code == 0 || !strings.Contains(stderr, "artifact name") {
			t.Errorf("artifact %q: exit %d: %s", name, code, stderr)
		}
	}
}

// buildTiny builds testdata/tiny the way build-release.sh builds the agent
// and returns the binary's path.
func buildTiny(t *testing.T, publicKeyVar, key string, tags ...string) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "tiny")
	args := []string{"build", "-trimpath", "-buildvcs=false", "-o", out,
		"-ldflags", "-s -w -X " + publicKeyVar + "=" + key}
	if len(tags) > 0 {
		args = append(args, "-tags", strings.Join(tags, ","))
	}
	cmd := exec.Command("go", append(args, "./testdata/tiny")...)
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	return out
}

// rawKey returns the base64 raw key that build-release.sh stamps for public.
func rawKey(t *testing.T, private string) string {
	t.Helper()
	return strings.TrimSpace(mustRun(t, "pubkey", "-key", private, "-raw"))
}

const publicKeyVar = "zimam/agent/internal/release.PublicKey"

func TestCheckBinaryFindsE2ETag(t *testing.T) {
	private, public := keyPair(t)
	key := rawKey(t, private)
	if _, stderr, code := runTool(t, "check-binary", "-key", public, buildTiny(t, publicKeyVar, key)); code != 0 {
		t.Fatalf("release build refused: %s", stderr)
	}
	for _, tags := range [][]string{{"e2e"}, {"netgo", "e2e"}} {
		_, stderr, code := runTool(t, "check-binary", "-key", public, buildTiny(t, publicKeyVar, key, tags...))
		if code == 0 || !strings.Contains(stderr, "e2e") {
			t.Fatalf("-tags %s: exit %d: %s", strings.Join(tags, ","), code, stderr)
		}
	}
}

func TestCheckBinaryNeedsTheReleaseKey(t *testing.T) {
	private, public := keyPair(t)
	otherPrivate, _ := keyPair(t)
	for name, binary := range map[string]string{
		"another key":    buildTiny(t, publicKeyVar, rawKey(t, otherPrivate)),
		"misspelled var": buildTiny(t, "zimam/agent/internal/release.PublicKy", rawKey(t, private)),
	} {
		_, stderr, code := runTool(t, "check-binary", "-key", public, binary)
		if code == 0 || !strings.Contains(stderr, "release key") {
			t.Errorf("%s: exit %d: %s", name, code, stderr)
		}
	}
}

func TestUsage(t *testing.T) {
	for _, args := range [][]string{nil, {"nope"}, {"sign"}, {"verify", "-key", "k"}} {
		if _, stderr, code := runTool(t, args...); code != 2 || !strings.Contains(stderr, "usage") {
			t.Errorf("release %s: exit %d: %s", strings.Join(args, " "), code, stderr)
		}
	}
}
