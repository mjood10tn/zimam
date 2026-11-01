package php

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"

	"zimam/agent/internal/configtx"
	"zimam/agent/internal/sysexec"
	"zimam/agent/internal/templates"
)

const (
	aptGet          = "/usr/bin/apt-get"
	maxKeyringBytes = 1 << 20
)

// aptLockWait makes apt-get wait for a dpkg lock that another job or
// unattended-upgrades holds, instead of failing at once.
var aptLockWait = []string{"-o", "DPkg::Lock::Timeout=300"}

var aptEnv = []string{"DEBIAN_FRONTEND=noninteractive"}

// EnsureRepo sets up the sury repository (§8.2). It downloads
// <php_repo_url>apt.gpg and checks its fingerprint before writing anything,
// then writes the keyring, the deb822 source and the pin through configtx,
// with apt-get update as the test. Each run fetches the keyring again, so a
// key whose expiry sury has extended gets refreshed.
//
// Bootstrap, php.install and maintenance's weekly refresh all call it. It
// holds the repository lock throughout, so their transactions on the same
// three files never overlap. The locks directory must exist.
func EnsureRepo(ctx context.Context, d sysexec.Deps) error {
	unlock, err := sysexec.Lock(ctx, d.Paths.AptRepoLock())
	if err != nil {
		return err
	}
	defer unlock()
	repo, err := url.Parse(d.Config.PHPRepoURL)
	if err != nil {
		return err
	}
	keyURL := repo.JoinPath("apt.gpg").String()
	d.Logf("Downloading %s", keyURL)
	raw, err := download(ctx, d.HTTPClient(), keyURL)
	if err != nil {
		return err
	}
	keyring, fpr, err := readKeyring(raw)
	if err != nil {
		return fmt.Errorf("%s: %w", keyURL, err)
	}
	if fpr != SuryFingerprint {
		return fmt.Errorf("%s holds key %s, not sury's %s", keyURL, fpr, SuryFingerprint)
	}
	data := templates.SuryRepo{URL: d.Config.PHPRepoURL, Host: repo.Hostname()}
	sources, err := templates.Render("apt/zimam-sury.sources", data)
	if err != nil {
		return err
	}
	pin, err := templates.Render("apt/zimam-sury.pref", data)
	if err != nil {
		return err
	}
	changes := []configtx.Change{
		configtx.RootFile(d.Paths.SuryKeyring(), keyring, 0o644),
		configtx.RootFile(d.Paths.SurySources(), sources, 0o644),
		configtx.RootFile(d.Paths.SuryPin(), pin, 0o644),
	}
	for _, c := range changes {
		if err := os.MkdirAll(filepath.Dir(c.Path), 0o755); err != nil {
			return err
		}
	}
	d.Logf("Writing the sury keyring, source and pin, then running apt-get update")
	return configtx.Apply(ctx, changes, func(ctx context.Context) error {
		return configtx.RunTest(ctx, d.Run, sysexec.Cmd{Path: aptGet, Args: append(slices.Clone(aptLockWait), "update"), Env: aptEnv})
	})
}

// download fetches a small file, such as a keyring, over HTTPS.
func download(ctx context.Context, c *http.Client, u string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("downloading %s: %s", u, resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxKeyringBytes+1))
	if err != nil {
		return nil, fmt.Errorf("downloading %s: %w", u, err)
	}
	if len(b) > maxKeyringBytes {
		return nil, fmt.Errorf("%s is larger than %d bytes", u, maxKeyringBytes)
	}
	return b, nil
}
