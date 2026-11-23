// Package composer keeps the pinned composer.phar (R02-D6): root downloads
// it once over HTTPS, checks its SHA-256 against the pin compiled into the
// agent, and keeps it under /usr/local/lib/zimam/composer. Composer itself
// only ever runs as a site user, through the helper.
package composer

import (
	"context"
	"fmt"
	"os"

	"zimam/agent/internal/fetch"
	"zimam/agent/internal/sysexec"
)

// maxPharBytes caps the download: 2.11.2 is 3.8 MB.
const maxPharBytes = 32 << 20

// tempPattern names a download in progress, in the Composer directory.
const tempPattern = ".composer-*"

// Ensure returns the path of the pinned phar, downloading it first when it
// is missing or its hash doesn't match the pin.
func Ensure(ctx context.Context, d sysexec.Deps) (path string, err error) {
	return ensure(ctx, d, pinned)
}

func ensure(ctx context.Context, d sysexec.Deps, p release) (string, error) {
	// Held throughout, so one job's download never removes another's
	// temporary file below.
	unlock, err := sysexec.Lock(ctx, d.Paths.ComposerLock())
	if err != nil {
		return "", err
	}
	defer unlock()
	if err := fetch.RemoveStaleTemps(d.Paths.ComposerDir(), tempPattern); err != nil {
		return "", err
	}
	path := d.Paths.ComposerPhar(p.version)
	switch ok, err := fetch.Matches(path, p.sha256, maxPharBytes); {
	case err != nil:
		return "", err
	case ok:
		return path, nil
	}
	dir := d.Paths.ComposerDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		return "", err
	}
	d.Logf("Downloading Composer %s from %s", p.version, p.url)
	tmp, err := os.CreateTemp(dir, tempPattern)
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name()) // fails harmlessly once renamed
	sum, err := fetch.Download(ctx, fetch.HTTPSOnly(d.HTTPClient()), p.url, tmp, maxPharBytes)
	if err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", err
	}
	if sum != p.sha256 {
		return "", fmt.Errorf("%s has SHA-256 %s, not the pinned %s", p.url, sum, p.sha256)
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return "", err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return "", err
	}
	return path, nil
}
