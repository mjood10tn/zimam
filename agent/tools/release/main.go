// Command release makes and checks Zimam releases (design §3). It runs on the
// build machine and in release.yml, never on a server:
//
//	release keygen -out <dir>
//	release pubkey -key <private-key> [-raw]
//	release manifest -version <v> -expires <RFC 3339 time> -composer <composer.json> -out <file> <artifact>...
//	release sign -key <private-key> <file>
//	release verify -key <public-key> -agent-version <v> <dir>
//	release check-binary -key <public-key> <binary>...
//
// Private keys are PKCS #8 PEM and public keys PEM SubjectPublicKeyInfo, the
// forms openssl reads. A signature is raw Ed25519 over a file's exact bytes,
// written beside it as <file>.sig.
package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/x509"
	"debug/buildinfo"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"zimam/agent/contract"
	"zimam/agent/internal/release"
)

type command struct {
	usage string
	run   func(args []string, stdout, stderr io.Writer) error
}

var commands = map[string]command{
	"keygen":       {"keygen -out <dir>", keygen},
	"pubkey":       {"pubkey -key <private-key> [-raw]", pubkey},
	"manifest":     {"manifest -version <v> -expires <RFC 3339 time> -composer <composer.json> -out <file> <artifact>...", manifest},
	"sign":         {"sign -key <private-key> <file>", sign},
	"verify":       {"verify -key <public-key> -agent-version <v> <dir>", verify},
	"check-binary": {"check-binary -key <public-key> <binary>...", checkBinary},
}

// errUsage makes run print the subcommand's usage and exit 2.
var errUsage = errors.New("usage")

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	var c command
	ok := len(args) > 0
	if ok {
		c, ok = commands[args[0]]
	}
	if !ok {
		for _, name := range slices.Sorted(maps.Keys(commands)) {
			fmt.Fprintln(stderr, "usage: release", commands[name].usage)
		}
		return 2
	}
	err := c.run(args[1:], stdout, stderr)
	switch {
	case errors.Is(err, errUsage):
		fmt.Fprintln(stderr, "usage: release", c.usage)
		return 2
	case err != nil:
		fmt.Fprintf(stderr, "release %s: %v\n", args[0], err)
		return 1
	}
	return 0
}

// flags returns a subcommand's flag set; parse errors go to stderr.
func flags(name string, stderr io.Writer) *flag.FlagSet {
	f := flag.NewFlagSet(name, flag.ContinueOnError)
	f.SetOutput(stderr)
	f.Usage = func() {}
	return f
}

// keygen writes a new key pair: release-key (0600) and release-key.pub. It
// refuses to replace either file.
func keygen(args []string, stdout, stderr io.Writer) error {
	f := flags("keygen", stderr)
	out := f.String("out", "", "")
	if f.Parse(args) != nil || *out == "" || f.NArg() != 0 {
		return errUsage
	}
	private := filepath.Join(*out, "release-key")
	public := private + ".pub"
	for _, name := range []string{private, public} {
		if _, err := os.Lstat(name); !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("%s already exists", name)
		}
	}
	pub, priv, err := ed25519.GenerateKey(nil)
	if err != nil {
		return err
	}
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return err
	}
	if err := writeNew(private, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0o600); err != nil {
		return err
	}
	pubPEM, err := publicPEM(pub)
	if err != nil {
		return err
	}
	if err := writeNew(public, pubPEM, 0o644); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "wrote %s (keep it secret) and %s\n", private, public)
	return nil
}

// pubkey prints the public key of a private key, as PEM or, with -raw, as
// the base64 of the 32 raw bytes that release.PublicKey holds.
func pubkey(args []string, stdout, stderr io.Writer) error {
	f := flags("pubkey", stderr)
	key := f.String("key", "", "")
	raw := f.Bool("raw", false, "")
	if f.Parse(args) != nil || *key == "" || f.NArg() != 0 {
		return errUsage
	}
	priv, err := readPrivateKey(*key)
	if err != nil {
		return err
	}
	pub := priv.Public().(ed25519.PublicKey)
	if *raw {
		_, err = fmt.Fprintln(stdout, base64.StdEncoding.EncodeToString(pub))
		return err
	}
	pubPEM, err := publicPEM(pub)
	if err == nil {
		_, err = stdout.Write(pubPEM)
	}
	return err
}

// phpVersion is the form of panel_php_min: the PHP versions bootstrap
// compares it with are <major>.<minor> (design §8.1).
var phpVersion = regexp.MustCompile(`^[0-9]+\.[0-9]+$`)

// manifest writes manifest.json for the artifacts, each listed by its base
// name. The release ships its agent and panel together, so the protocol is
// the newest one the agent speaks and min_agent_version is the release's own
// version. The same inputs always give the same bytes.
func manifest(args []string, stdout, stderr io.Writer) error {
	f := flags("manifest", stderr)
	version := f.String("version", "", "")
	expires := f.String("expires", "", "")
	composer := f.String("composer", "", "")
	out := f.String("out", "", "")
	if f.Parse(args) != nil || *version == "" || *expires == "" || *composer == "" || *out == "" || f.NArg() == 0 {
		return errUsage
	}
	if !release.ValidVersion(*version) {
		return fmt.Errorf("version %q is not a SemVer version", *version)
	}
	exp, err := time.Parse(time.RFC3339, *expires)
	if err != nil {
		return fmt.Errorf("expires: %w", err)
	}
	php, err := panelPHPMin(*composer)
	if err != nil {
		return err
	}
	m := release.Manifest{
		Version:         *version,
		Protocol:        slices.Max(contract.SupportedProtocols),
		MinAgentVersion: *version,
		PanelPHPMin:     php,
		Expires:         exp.UTC().Truncate(time.Second),
		Artifacts:       map[string]string{},
	}
	outPath, err := filepath.Abs(*out)
	if err != nil {
		return err
	}
	for _, name := range f.Args() {
		base := filepath.Base(name)
		if p, err := filepath.Abs(name); err != nil || p == outPath {
			return fmt.Errorf("the manifest can't list itself")
		}
		if _, dup := m.Artifacts[base]; dup {
			return fmt.Errorf("two artifacts are named %s", base)
		}
		if m.Artifacts[base], err = fileSHA256(name); err != nil {
			return err
		}
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(*out, append(data, '\n'), 0o644)
}

// panelPHPMin returns composer.json's config.platform.php, after checking
// that the composer.lock beside it was resolved for the same PHP. The
// bundled vendor/ then runs on every PHP from that version up (D7).
func panelPHPMin(composerJSON string) (string, error) {
	var c struct {
		Config struct {
			Platform struct {
				PHP string `json:"php"`
			} `json:"platform"`
		} `json:"config"`
	}
	if err := readJSON(composerJSON, &c); err != nil {
		return "", err
	}
	php := c.Config.Platform.PHP
	if !phpVersion.MatchString(php) {
		return "", fmt.Errorf("%s: config.platform.php is %q, want <major>.<minor>", composerJSON, php)
	}
	var lock struct {
		PlatformOverrides struct {
			PHP string `json:"php"`
		} `json:"platform-overrides"`
	}
	lockFile := filepath.Join(filepath.Dir(composerJSON), "composer.lock")
	if err := readJSON(lockFile, &lock); err != nil {
		return "", err
	}
	if lock.PlatformOverrides.PHP != php {
		return "", fmt.Errorf("%s was resolved for PHP %q, but config.platform.php is %s: run composer update --lock",
			lockFile, lock.PlatformOverrides.PHP, php)
	}
	return php, nil
}

// sign writes <file>.sig, a raw Ed25519 signature over the file's bytes.
func sign(args []string, stdout, stderr io.Writer) error {
	f := flags("sign", stderr)
	key := f.String("key", "", "")
	if f.Parse(args) != nil || *key == "" || f.NArg() != 1 {
		return errUsage
	}
	priv, err := readPrivateKey(*key)
	if err != nil {
		return err
	}
	data, err := os.ReadFile(f.Arg(0))
	if err != nil {
		return err
	}
	return os.WriteFile(f.Arg(0)+".sig", ed25519.Sign(priv, data), 0o644)
}

// artifactName is a plain file name, so a manifest never points outside
// the release directory.
var artifactName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]*$`)

// verify checks a release directory as a server would: the manifest with
// release.Verify, then every artifact it lists.
func verify(args []string, stdout, stderr io.Writer) error {
	f := flags("verify", stderr)
	key := f.String("key", "", "")
	agentVersion := f.String("agent-version", "", "")
	if f.Parse(args) != nil || *key == "" || *agentVersion == "" || f.NArg() != 1 {
		return errUsage
	}
	pub, err := readPublicKey(*key)
	if err != nil {
		return err
	}
	dir := f.Arg(0)
	data, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return err
	}
	sig, err := os.ReadFile(filepath.Join(dir, "manifest.json.sig"))
	if err != nil {
		return err
	}
	m, err := release.Verify(pub, data, sig, time.Now(), *agentVersion)
	if err != nil {
		return err
	}
	for _, name := range slices.Sorted(maps.Keys(m.Artifacts)) {
		if !artifactName.MatchString(name) {
			return fmt.Errorf("artifact name %q is not a plain file name", name)
		}
		if err := checkArtifact(m, dir, name); err != nil {
			return err
		}
	}
	// release.yml publishes every file in the directory, so it must hold
	// nothing the signed manifest doesn't list.
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		name := e.Name()
		if _, listed := m.Artifacts[name]; !listed && name != "manifest.json" && name != "manifest.json.sig" {
			return fmt.Errorf("%s is in the release directory but not in the manifest", name)
		}
	}
	fmt.Fprintf(stdout, "release %s and its %d artifacts verified\n", m.Version, len(m.Artifacts))
	return nil
}

func checkArtifact(m *release.Manifest, dir, name string) error {
	file, err := os.Open(filepath.Join(dir, name))
	if err != nil {
		return err
	}
	defer file.Close()
	return m.CheckArtifact(name, file)
}

// checkBinary refuses an agent built with the e2e tag or without the
// release key stamped into release.PublicKey (design §3, §14.4). Release
// builds use -trimpath, which keeps -ldflags out of the build info, so the
// key is looked for in the binary's bytes: a misspelled -X stamps nothing.
// The build info is read, never run, so an arm64 agent is checked on amd64.
func checkBinary(args []string, stdout, stderr io.Writer) error {
	f := flags("check-binary", stderr)
	key := f.String("key", "", "")
	if f.Parse(args) != nil || *key == "" || f.NArg() == 0 {
		return errUsage
	}
	pub, err := readPublicKey(*key)
	if err != nil {
		return err
	}
	stamped := []byte(base64.StdEncoding.EncodeToString(pub))
	for _, name := range f.Args() {
		if err := checkOneBinary(name, stamped); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
		fmt.Fprintf(stdout, "%s: a release build with the release key\n", name)
	}
	return nil
}

func checkOneBinary(name string, stamped []byte) error {
	info, err := buildinfo.ReadFile(name)
	if err != nil {
		return err
	}
	for _, s := range info.Settings {
		if s.Key == "-tags" && slices.Contains(strings.Split(s.Value, ","), "e2e") {
			return fmt.Errorf("built with the e2e tag (-tags=%s)", s.Value)
		}
	}
	data, err := os.ReadFile(name)
	if err != nil {
		return err
	}
	if !bytes.Contains(data, stamped) {
		return errors.New("the release key isn't stamped into release.PublicKey")
	}
	return nil
}

func readPrivateKey(name string) (ed25519.PrivateKey, error) {
	der, err := readPEM(name, "PRIVATE KEY")
	if err != nil {
		return nil, err
	}
	key, err := x509.ParsePKCS8PrivateKey(der)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	priv, ok := key.(ed25519.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("%s is not an Ed25519 key", name)
	}
	return priv, nil
}

func readPublicKey(name string) (ed25519.PublicKey, error) {
	der, err := readPEM(name, "PUBLIC KEY")
	if err != nil {
		return nil, err
	}
	key, err := x509.ParsePKIXPublicKey(der)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	pub, ok := key.(ed25519.PublicKey)
	if !ok {
		return nil, fmt.Errorf("%s is not an Ed25519 key", name)
	}
	return pub, nil
}

// readPEM returns the DER of the file's first PEM block, which must have
// the given type. Errors never quote the file's content.
func readPEM(name, blockType string) ([]byte, error) {
	data, err := os.ReadFile(name)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(data)
	if block == nil || block.Type != blockType {
		return nil, fmt.Errorf("%s is not a PEM %s", name, blockType)
	}
	return block.Bytes, nil
}

func publicPEM(pub ed25519.PublicKey) ([]byte, error) {
	der, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}), nil
}

// writeNew creates name with data, failing if it already exists.
func writeNew(name string, data []byte, perm os.FileMode) error {
	file, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		file.Close()
		return err
	}
	return file.Close()
}

func fileSHA256(name string) (string, error) {
	file, err := os.Open(name)
	if err != nil {
		return "", err
	}
	defer file.Close()
	h := sha256.New()
	if _, err := io.Copy(h, file); err != nil {
		return "", err
	}
	return "sha256:" + hex.EncodeToString(h.Sum(nil)), nil
}

func readJSON(name string, v any) error {
	data, err := os.ReadFile(name)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, v); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}
