// Package node installs Node.js from NodeSource (R04-D6): the APT
// repository for one major, the nodejs package, and its removal.
package node

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"zimam/agent/contract"
	"zimam/agent/internal/configtx"
	"zimam/agent/internal/php"
	"zimam/agent/internal/sysexec"
	"zimam/agent/internal/templates"
)

// Fingerprint is the primary key fingerprint of NodeSource's signing key
// (rsa2048, 2016-05-23, "NSolid <nsolid-gpg@nodesource.com>"). NodeSource
// publishes none, as sury does not, so the value comes from the key itself
// (downloaded 2026-11-18, sha256 b42e0321...271d) and agrees with the pins
// third parties publish.
const Fingerprint = "6F71F525282841EEDAF851B42F59B5F99B1BE0B4"

// KeyURL is where NodeSource serves its key, ASCII-armored.
const KeyURL = "https://deb.nodesource.com/gpgkey/nodesource-repo.gpg.key"

// EnsureRepo sets up the NodeSource repository for major (R04-D6). As
// php.EnsureRepo does for sury, it downloads the key, checks that it is one
// certificate with the pinned fingerprint, and then writes the keyring
// (dearmored: APT ignores an armored file named .gpg), the deb822 source and
// the pin through configtx, with apt-get update as the test. It writes the
// checked bytes, never the raw download, which could hold a second key
// behind the first. It holds the repository lock throughout.
func EnsureRepo(ctx context.Context, d sysexec.Deps, major contract.NodeMajor) error {
	unlock, err := sysexec.Lock(ctx, d.Paths.AptRepoLock())
	if err != nil {
		return err
	}
	defer unlock()
	return ensureRepoLocked(ctx, d, major)
}

// ensureRepoLocked is EnsureRepo for a caller that holds the repository lock.
func ensureRepoLocked(ctx context.Context, d sysexec.Deps, major contract.NodeMajor) error {
	d.Logf("Downloading %s", KeyURL)
	raw, err := php.Download(ctx, d.HTTPClient(), KeyURL)
	if err != nil {
		return err
	}
	fpr, err := php.CheckSingleCertificate(raw)
	if err != nil {
		return fmt.Errorf("%s: %w", KeyURL, err)
	}
	keyring, err := php.BinaryKeyring(raw)
	if err != nil {
		return fmt.Errorf("%s: %w", KeyURL, err)
	}
	if fpr != Fingerprint {
		return fmt.Errorf("%s holds key %s, not NodeSource's %s", KeyURL, fpr, Fingerprint)
	}
	sources, err := templates.Render("apt/zimam-nodesource.sources", templates.NodeRepo{Major: major})
	if err != nil {
		return err
	}
	pin, err := templates.Render("apt/zimam-nodesource.pref", nil)
	if err != nil {
		return err
	}
	changes := []configtx.Change{
		configtx.RootFile(d.Paths.NodeKeyring(), keyring, 0o644),
		configtx.RootFile(d.Paths.NodeSources(), sources, 0o644),
		configtx.RootFile(d.Paths.NodePin(), pin, 0o644),
	}
	for _, c := range changes {
		if err := os.MkdirAll(filepath.Dir(c.Path), 0o755); err != nil {
			return err
		}
	}
	d.Logf("Writing the NodeSource keyring, source and pin, then running apt-get update")
	return configtx.Apply(ctx, changes, func(ctx context.Context) error {
		return configtx.RunTest(ctx, d.Run, sysexec.Cmd{Path: php.AptGet, Args: append(slices.Clone(php.AptLockWait), "update"), Env: php.AptEnv})
	})
}

// RefreshRepo downloads the key again and rewrites the repository files for
// the major APT follows now (D23), for maintenance's weekly refresh. Without
// the source file there is nothing to refresh.
// The major is read under the repository lock: a node.install that changes it
// meanwhile must not be written back to the old one.
func RefreshRepo(ctx context.Context, d sysexec.Deps) error {
	unlock, err := sysexec.Lock(ctx, d.Paths.AptRepoLock())
	if err != nil {
		return err
	}
	defer unlock()
	major, err := RepoMajor(d.Paths)
	if err != nil || major == "" {
		return err
	}
	return ensureRepoLocked(ctx, d, major)
}
