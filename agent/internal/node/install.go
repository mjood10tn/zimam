package node

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"zimam/agent/contract"
	"zimam/agent/internal/configtx"
	"zimam/agent/internal/php"
	"zimam/agent/internal/sysexec"
)

// Install installs Node.js from NodeSource's major (R04-D6, D19). A
// different installed major is purged first: going from 26 to 24 would be a
// downgrade. The same major is upgraded in place, which is what its Update
// does. Afterwards the version is checked: the pin forbids Ubuntu's own
// nodejs, but a wrong package never passes as installed.
func Install(ctx context.Context, d sysexec.Deps, major contract.NodeMajor) (contract.NodeInstallResult, error) {
	if err := major.Validate(); err != nil {
		return contract.NodeInstallResult{}, fmt.Errorf("Node %q %v", major, err)
	}
	have, err := Installed(ctx, d)
	if err != nil {
		return contract.NodeInstallResult{}, err
	}
	if have != "" && MajorOf(have) != major {
		d.Logf("Purging Node.js %s before installing %s", have, major)
		if err := purge(ctx, d); err != nil {
			return contract.NodeInstallResult{}, err
		}
	}
	if err := EnsureRepo(ctx, d, major); err != nil {
		return contract.NodeInstallResult{}, err
	}
	d.Logf("Installing Node.js %s", major)
	// Never stop at a dpkg question about a changed config file.
	args := append(slices.Clone(php.AptLockWait), "-o", "Dpkg::Options::=--force-confdef", "-o", "Dpkg::Options::=--force-confold",
		"install", "-y", "--no-install-recommends", "nodejs")
	if err := php.AptGetRepairing(ctx, d, args); err != nil {
		return contract.NodeInstallResult{}, err
	}
	version, err := Installed(ctx, d)
	if err != nil {
		return contract.NodeInstallResult{}, err
	}
	if !strings.HasPrefix(version, string(major)+".") || !strings.Contains(version, "nodesource") {
		return contract.NodeInstallResult{}, fmt.Errorf("apt installed nodejs %q, not a NodeSource %s.x package", version, major)
	}
	return contract.NodeInstallResult{Major: major, Version: version}, nil
}

func purge(ctx context.Context, d sysexec.Deps) error {
	return php.AptGetRepairing(ctx, d, append(slices.Clone(php.AptLockWait), "purge", "-y", "nodejs"))
}

// Uninstall purges nodejs and removes the repository's source, pin and
// keyring. The caller checks first that nothing is using Node.
func Uninstall(ctx context.Context, d sysexec.Deps) error {
	d.Logf("Purging nodejs")
	if err := purge(ctx, d); err != nil {
		return err
	}
	unlock, err := sysexec.Lock(ctx, d.Paths.AptRepoLock())
	if err != nil {
		return err
	}
	defer unlock()
	d.Logf("Removing the NodeSource keyring, source and pin")
	return configtx.Apply(ctx, []configtx.Change{
		{Path: d.Paths.NodeSources(), Remove: true},
		{Path: d.Paths.NodePin(), Remove: true},
		{Path: d.Paths.NodeKeyring(), Remove: true},
	}, func(context.Context) error { return nil })
}
