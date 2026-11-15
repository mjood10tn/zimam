// Package composer keeps the pinned composer.phar (R02-D6): root downloads
// it once over HTTPS, checks its SHA-256 against the pin compiled into the
// agent, and keeps it under /usr/local/lib/zimam/composer. Composer itself
// only ever runs as a site user, through the helper.
package composer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"net/http"
	"os"

	"zimam/agent/internal/sysexec"
)

// maxPharBytes caps the download: 2.11.2 is 3.8 MB.
const maxPharBytes = 32 << 20

// Ensure returns the path of the pinned phar, downloading it first when it
// is missing or its hash doesn't match the pin.
func Ensure(ctx context.Context, d sysexec.Deps) (path string, err error) {
	return ensure(ctx, d, pinned)
}

func ensure(ctx context.Context, d sysexec.Deps, p pin) (string, error) {
	path := d.Paths.ComposerPhar(p.version)
	switch ok, err := matches(path, p.sha256); {
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
	tmp, err := os.CreateTemp(dir, ".composer-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name()) // fails harmlessly once renamed
	sum, err := download(ctx, d.HTTPClient(), p.url, tmp)
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

// matches reports whether path is a regular file with the SHA-256 want. A
// missing file is false without an error.
func matches(path, want string) (bool, error) {
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return false, err
	}
	if !fi.Mode().IsRegular() {
		return false, fmt.Errorf("%s is not a regular file", path)
	}
	h := sha256.New()
	if _, err := io.Copy(h, io.LimitReader(f, maxPharBytes+1)); err != nil {
		return false, err
	}
	return hexSum(h) == want, nil
}

// download writes u's body to w, at most maxPharBytes, and returns its
// SHA-256.
func download(ctx context.Context, c *http.Client, u string, w io.Writer) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", err
	}
	resp, err := c.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("downloading %s: %s", u, resp.Status)
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(w, h), io.LimitReader(resp.Body, maxPharBytes+1))
	if err != nil {
		return "", fmt.Errorf("downloading %s: %w", u, err)
	}
	if n > maxPharBytes {
		return "", fmt.Errorf("%s is larger than %d bytes", u, maxPharBytes)
	}
	return hexSum(h), nil
}

func hexSum(h hash.Hash) string { return hex.EncodeToString(h.Sum(nil)) }
