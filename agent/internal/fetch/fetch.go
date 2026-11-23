// Package fetch downloads release artefacts whose SHA-256 is compiled into
// the agent, and checks files against such a pin: Composer's phar (R02-D6)
// and rclone's zip and binary (R05-D1).
package fetch

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
)

// HTTPSOnly is c with redirects allowed only to https URLs; c's own
// redirect policy still applies on top.
func HTTPSOnly(c *http.Client) *http.Client {
	copied := *c
	next := c.CheckRedirect
	copied.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if req.URL.Scheme != "https" {
			return fmt.Errorf("refusing a redirect to %s: not https", req.URL.Redacted())
		}
		if next != nil {
			return next(req, via)
		}
		if len(via) >= 10 {
			return errors.New("stopped after 10 redirects")
		}
		return nil
	}
	return &copied
}

// RemoveStaleTemps deletes the files matching pattern in dir: downloads
// that a killed run left behind.
func RemoveStaleTemps(dir, pattern string) error {
	stale, err := filepath.Glob(filepath.Join(dir, pattern))
	if err != nil {
		return err
	}
	for _, name := range stale {
		if err := os.Remove(name); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	return nil
}

// Matches reports whether path is a regular file with the SHA-256 want,
// reading at most max+1 bytes. A missing file is false without an error.
func Matches(path, want string, max int64) (bool, error) {
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
	sum, _, err := Copy(io.Discard, f, max)
	if errors.Is(err, ErrTooLarge) {
		return false, nil // larger than the pinned file, so not it
	}
	return err == nil && sum == want, err
}

// Download writes u's body to w, at most max bytes, and returns its SHA-256.
func Download(ctx context.Context, c *http.Client, u string, w io.Writer, max int64) (string, error) {
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
	sum, _, err := Copy(w, resp.Body, max)
	if err != nil {
		return "", fmt.Errorf("downloading %s: %w", u, err)
	}
	return sum, nil
}

// ErrTooLarge reports a stream longer than its cap.
var ErrTooLarge = errors.New("larger than the pinned size cap")

// Copy copies at most max bytes of r to w and returns their SHA-256 and
// count. A longer stream is ErrTooLarge.
func Copy(w io.Writer, r io.Reader, max int64) (string, int64, error) {
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(w, h), io.LimitReader(r, max+1))
	if err != nil {
		return "", n, err
	}
	if n > max {
		return "", n, fmt.Errorf("%w (%d bytes)", ErrTooLarge, max)
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}
