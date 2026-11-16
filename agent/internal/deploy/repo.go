// Package deploy connects sites to their GitHub repositories and deploys
// them (R02-D3 to R02-D7). Everything inside a site's home happens through
// the site-user helper; root never reads the deploy key.
package deploy

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"

	"zimam/agent/contract"
	"zimam/agent/internal/helper"
	"zimam/agent/internal/php"
	"zimam/agent/internal/siteexec"
	"zimam/agent/internal/sites"
	"zimam/agent/internal/sysexec"
)

// Paths inside a site's home, relative to it, as the helper takes them
// (Site layout).
const (
	sshDir     = ".ssh"
	keyFile    = ".ssh/zimam_deploy"
	knownHosts = ".ssh/known_hosts"
	// MirrorDir is the site's bare repository, fetched as the site user.
	MirrorDir = "site/repo.git"
)

// GitSSHCommand is how git reaches GitHub (R02-D3, spike amendment A5):
// only the site's deploy key, only the pinned host keys in the site's own
// known_hosts (the global file is ignored), and never a prompt. git runs it
// through /bin/sh as the site user (A6); it is a constant, never built from
// panel text.
const GitSSHCommand = "ssh -i ~/.ssh/zimam_deploy -o IdentitiesOnly=yes -o StrictHostKeyChecking=yes " +
	"-o UserKnownHostsFile=~/.ssh/known_hosts -o GlobalKnownHostsFile=/dev/null -o BatchMode=yes"

// GitHubKnownHosts is GitHub's SSH host keys (R02-D3), checked on
// 2026-11-15 against GitHub's documentation and api.github.com/meta
// (spike amendment A5). A key rotation by GitHub needs a Zimam release.
const GitHubKnownHosts = "" +
	"github.com ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMqqnkVzrm0SdG6UOoqKLsabgH5C9okWi0dh2l9GKJl\n" +
	"github.com ecdsa-sha2-nistp256 AAAAE2VjZHNhLXNoYTItbmlzdHAyNTYAAAAIbmlzdHAyNTYAAABBBEmKSENjQEezOmxkZMy7opKgwFB9nkt5YRrYMjNuG5N87uRgg6CLrbo5wAdT/y6v0mKV0U2w0WZ2YB/++Tpockg=\n" +
	"github.com ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABgQCj7ndNxQowgcQnjshcLrqPEiiphnt+VTTvDP6mHBL9j1aNUkY4Ue1gvwnGLVlOhGeYrnZaMgRK6+PKCUXaDbC7qtbW8gIkhL7aGCsOr/C56SJMy/BCZfxd1nWzAOxSDPgVsmerOBYfNqltV9/hWCqBywINIR+5dIg6JTJ72pcEpEjcYgXkE2YEFXV1JHnsKgbLWNlhScqb2UmyRkQyytRLtL+38TGxkxCflmO+5Z8CSSNY7GidjMIZ7Q4zMjA2n1nGrlTDkzwDCsw+wqFPGQA179cnfGWOWRVruj16z6XyvxvjJwbz0wQZ75XK5tKSb7FNyeIEs4TT4jk+S4dhPeAUC5y+bDYirYgM4GC7uEnztnZyaVWQ7B381AK4Qdrwt51ZqExKbQpTUNn+EjqoTwvqNj4kqx5QUCI0ThS/YkOxJCXmPUWZbhjpCg56i+2aB6CmK2JGhn57K5mj0MNdBXA4/WnwH6XoPWJzK5Nyu2zB3nAZp+S5hpQs+p1vN1/wsjk=\n"

// RemoteURL is the SSH URL git fetches a repository from; the agent adds
// the .git (Repository refuses one).
func RemoteURL(r contract.Repository) string {
	return "git@github.com:" + string(r) + ".git"
}

// maxPublicKeyBytes caps what ssh-keygen -y may print: an ed25519 line with
// a long comment is well under it.
const maxPublicKeyBytes = 4 << 10

// Helper is what deploys need of the site-user helper; *helper.Client is
// the real one.
type Helper interface {
	siteexec.Execer
	Mkdir(ctx context.Context, a helper.Account, name string, mode fs.FileMode) error
	Write(ctx context.Context, a helper.Account, name string, mode fs.FileMode, content io.Reader) error
	Stat(ctx context.Context, a helper.Account, name string) (helper.FileInfo, error)
	RemoveTree(ctx context.Context, a helper.Account, name string) error
}

// CheckRepo is site.repo's precheck: the site must exist.
func CheckRepo(d sysexec.Deps, p contract.SiteRepoParams) error {
	_, err := loadSite(d, p.Name)
	return err
}

func loadSite(d sysexec.Deps, name contract.SiteName) (sites.State, error) {
	st, err := sites.Load(d.Paths, name)
	if errors.Is(err, fs.ErrNotExist) {
		return sites.State{}, &contract.AgentError{Code: contract.ErrNotFound, Message: fmt.Sprintf("Site %s doesn't exist.", name)}
	}
	return st, err
}

// Repo connects a site to a GitHub repository and branch (site.repo):
//
//  1. git and unzip are installed if missing (R02-D7);
//  2. ~/.ssh is 0700, made through the helper;
//  3. the deploy key is kept while its private half exists and ssh-keygen
//     -y reads it, unless rotate_key is set (R02-D4); otherwise both
//     halves are deleted, since ssh-keygen won't overwrite (A7), and
//     ssh-keygen makes a new key as the site user. The public key always
//     comes from ssh-keygen -y, so it is the private key's own, and its
//     fingerprint is computed here; a missing .pub alone changes nothing;
//  4. ~/.ssh/known_hosts is GitHub's pinned keys, rewritten every time
//     (R02-D3);
//  6. a changed repository or branch drops the bare mirror, so the next
//     deploy starts fresh;
//  7. the state file records the repository, the branch and the
//     fingerprint.
//
// The result holds only the public key, without a comment, and its
// fingerprint.
func Repo(ctx context.Context, d sysexec.Deps, h Helper, lookup sites.Lookup, p contract.SiteRepoParams) (contract.SiteRepoResult, error) {
	if err := p.Validate(); err != nil {
		return contract.SiteRepoResult{}, err
	}
	st, err := loadSite(d, p.Name)
	if err != nil {
		return contract.SiteRepoResult{}, err
	}
	if err := ensurePackages(ctx, d); err != nil {
		return contract.SiteRepoResult{}, err
	}
	a, err := lookup("s_" + string(p.Name))
	if err != nil {
		return contract.SiteRepoResult{}, err
	}
	if err := h.Mkdir(ctx, a, sshDir, 0o700); err != nil {
		return contract.SiteRepoResult{}, err
	}
	pub, fp, err := ensureKey(ctx, d, h, a, p)
	if err != nil {
		return contract.SiteRepoResult{}, err
	}
	d.Logf("Writing GitHub's pinned host keys to ~/%s", knownHosts)
	if err := h.Write(ctx, a, knownHosts, 0o600, strings.NewReader(pinnedHosts(d.Config))); err != nil {
		return contract.SiteRepoResult{}, err
	}
	if st.Repository != p.Repository || st.Branch != p.Branch {
		// Dropped before the state changes: a mirror of the old repository
		// must never be fetched under the new one's name.
		d.Logf("Removing ~/%s, so the next deploy fetches %s of %s fresh", MirrorDir, p.Branch, p.Repository)
		if err := h.RemoveTree(ctx, a, MirrorDir); err != nil {
			return contract.SiteRepoResult{}, err
		}
	}
	st.Repository, st.Branch, st.DeployKeyFingerprint = p.Repository, p.Branch, fp
	if err := sites.Save(d.Paths, st); err != nil {
		return contract.SiteRepoResult{}, err
	}
	d.Logf("Site %s deploys %s from %s; its deploy key is %s", p.Name, p.Branch, p.Repository, fp)
	return contract.SiteRepoResult{Name: p.Name, Repository: p.Repository, Branch: p.Branch, PublicKey: pub, Fingerprint: fp}, nil
}

// ensurePackages installs git and unzip when either is missing (R02-D7).
func ensurePackages(ctx context.Context, d sysexec.Deps) error {
	var missing []string
	for _, pkg := range []struct{ name, bin string }{
		{"git", d.Paths.GitBinary()},
		{"unzip", d.Paths.UnzipBinary()},
	} {
		ok, err := exists(pkg.bin)
		if err != nil {
			return err
		}
		if !ok {
			missing = append(missing, pkg.name)
		}
	}
	return php.InstallPackages(ctx, d, missing...)
}

// ensureKey keeps the deploy key when its private half exists and
// ssh-keygen -y reads it, unless rotate_key is set; otherwise it makes a
// new one. It returns the public key and its fingerprint.
func ensureKey(ctx context.Context, d sysexec.Deps, h Helper, a helper.Account, p contract.SiteRepoParams) (key, fpr string, err error) {
	if !p.RotateKey {
		priv, err := present(ctx, h, a, keyFile)
		if err != nil {
			return "", "", err
		}
		if priv {
			key, fpr, err := publicKey(ctx, d, h, a)
			var exit *sysexec.ExitError
			switch {
			case err == nil:
				d.Logf("Keeping the site's deploy key")
				return key, fpr, nil
			case !errors.As(err, &exit):
				return "", "", err
			}
			d.Logf("ssh-keygen can't read the deploy key; replacing it")
		}
	}
	// ssh-keygen won't overwrite either half (A7). RemoveTree of a missing
	// name succeeds.
	for _, name := range []string{keyFile, keyFile + ".pub"} {
		if err := h.RemoveTree(ctx, a, name); err != nil {
			return "", "", err
		}
	}
	d.Logf("Making a new deploy key as %s", a.Name)
	comment := "zimam-" + string(p.Name) + "@" + string(d.Config.PanelDomain)
	if _, err := siteexec.Run(ctx, d, h, a, siteexec.Request{
		Program:   siteexec.SSHKeygen,
		Args:      []string{"-t", "ed25519", "-N", "", "-C", comment, "-f", keyFile, "-q"},
		LogPrefix: "ssh-keygen: ",
	}); err != nil {
		return "", "", err
	}
	return publicKey(ctx, d, h, a)
}

// publicKey reads the deploy key's public half as ssh-keygen -y derives it
// from the private key, and its SHA-256 fingerprint.
func publicKey(ctx context.Context, d sysexec.Deps, h Helper, a helper.Account) (key, fpr string, err error) {
	var out bytes.Buffer
	if _, err := siteexec.Run(ctx, d, h, a, siteexec.Request{
		Program:    siteexec.SSHKeygen,
		Args:       []string{"-y", "-f", keyFile},
		LogPrefix:  "ssh-keygen: ",
		Capture:    &out,
		CaptureMax: maxPublicKeyBytes,
	}); err != nil {
		return "", "", err
	}
	return parsePublicKey(out.Bytes())
}

// parsePublicKey checks that out is one ed25519 public key line, as
// ssh-keygen -y prints it, and returns the key without its comment and the
// key's fingerprint. The home is the site user's, so nothing in it is
// trusted: the key must decode to an ed25519 key of 32 bytes.
func parsePublicKey(out []byte) (key, fpr string, err error) {
	line, ok := bytes.CutSuffix(out, []byte("\n"))
	if !ok || bytes.ContainsAny(line, "\r\n") {
		return "", "", errors.New("ssh-keygen -y didn't print one line")
	}
	f := strings.Fields(string(line))
	if len(f) < 2 || f[0] != "ssh-ed25519" {
		return "", "", errors.New("the deploy key is not an ed25519 key")
	}
	blob, err := base64.StdEncoding.Strict().DecodeString(f[1])
	if err != nil {
		return "", "", fmt.Errorf("the deploy key's public half doesn't decode: %w", err)
	}
	if !isEd25519(blob) {
		return "", "", errors.New("the deploy key's public half is not an ed25519 key")
	}
	fp := fingerprint(blob)
	if !sites.KeyFingerprint.MatchString(fp) {
		return "", "", fmt.Errorf("fingerprint %q has the wrong shape", fp)
	}
	return "ssh-ed25519 " + base64.StdEncoding.EncodeToString(blob), fp, nil
}

// isEd25519 reports whether blob is an SSH ed25519 public key: the string
// "ssh-ed25519", then a 32-byte string, and nothing else.
func isEd25519(blob []byte) bool {
	const name = "ssh-ed25519"
	want := 4 + len(name) + 4 + 32
	if len(blob) != want {
		return false
	}
	if binary.BigEndian.Uint32(blob) != uint32(len(name)) || string(blob[4:4+len(name)]) != name {
		return false
	}
	return binary.BigEndian.Uint32(blob[4+len(name):]) == 32
}

// fingerprint is OpenSSH's SHA-256 fingerprint of a public key blob.
func fingerprint(blob []byte) string {
	sum := sha256.Sum256(blob)
	return "SHA256:" + base64.RawStdEncoding.EncodeToString(sum[:])
}

// present reports whether name exists in the home, as anything.
func present(ctx context.Context, h Helper, a helper.Account, name string) (bool, error) {
	_, err := h.Stat(ctx, a, name)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, fs.ErrNotExist):
		return false, nil
	default:
		return false, err
	}
}

// exists reports whether root's path exists; these are system binaries,
// outside every home.
func exists(path string) (bool, error) {
	_, err := os.Stat(path)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, fs.ErrNotExist):
		return false, nil
	default:
		return false, err
	}
}

var _ Helper = (*helper.Client)(nil)
