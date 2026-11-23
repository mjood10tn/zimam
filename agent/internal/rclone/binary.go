// Package rclone keeps the pinned rclone binary (R05-D1), renders
// rclone.conf (R05-D2) and runs rclone with that config and nothing else.
// rclone runs only as root: rclone.conf holds every destination's secrets.
package rclone

import (
	"archive/zip"
	"context"
	"fmt"
	"io"
	"os"
	"runtime"

	"zimam/agent/internal/fetch"
	"zimam/agent/internal/sysexec"
)

// pinVersion is the rclone release the agent uses (Task 1 spike). rclone
// ships Linux builds as zips; scripts/configcheck/Dockerfile pins the same.
const pinVersion = "1.76.0"

// Version is the pinned release, recorded in each backup's manifest.
const Version = pinVersion

// pins are the release's SHA-256s per architecture: the zip's, from
// rclone's PGP-signed SHA256SUMS, and the binary's inside it.
var pins = map[string]struct{ zip, binary string }{
	"amd64": {
		zip:    "b00f00b3266c3764413afe599d3ec316f6f43b3e0ea8a0bc1d186d2133e46df7",
		binary: "62e4d3e5432f2c685f1834b8bb342058ff0611c99d048aeb44bb8d8fd611527e",
	},
	"arm64": {
		zip:    "ee169016675e23a69d77240565133ff7dbc9b33d16fa8f7053d4af39f1eaeb78",
		binary: "ee07f4d157b53d7b3e4f428aa292b30c1958525eb43afa49291b9b6e988a0ea3",
	},
}

// Caps on the download and the extracted binary: 1.76.0's amd64 zip is
// 32 MB and its binary 86 MB.
const (
	maxZipBytes    = 128 << 20
	maxBinaryBytes = 256 << 20
)

// tempPattern names a download or extraction in progress, in RcloneDir.
const tempPattern = ".rclone-*"

// release is one rclone release for one architecture.
type release struct {
	version      string
	url          string
	member       string // the binary's path inside the zip
	zipSHA256    string // lowercase hex
	binarySHA256 string
}

// pinned is the pinned release for arch.
func pinned(arch string) (release, error) {
	p, ok := pins[arch]
	if !ok {
		return release{}, fmt.Errorf("no rclone pin for %s", arch)
	}
	name := "rclone-v" + pinVersion + "-linux-" + arch
	return release{
		version:      pinVersion,
		url:          "https://downloads.rclone.org/v" + pinVersion + "/" + name + ".zip",
		member:       name + "/rclone",
		zipSHA256:    p.zip,
		binarySHA256: p.binary,
	}, nil
}

// Ensure returns the path of the pinned rclone binary after checking its
// hash, downloading and extracting it first when it is missing or changed.
func Ensure(ctx context.Context, d sysexec.Deps) (string, error) {
	r, err := pinned(runtime.GOARCH)
	if err != nil {
		return "", err
	}
	return ensure(ctx, d, r)
}

func ensure(ctx context.Context, d sysexec.Deps, r release) (string, error) {
	// Held throughout, so one job's download never removes another's
	// temporary files below.
	unlock, err := sysexec.Lock(ctx, d.Paths.RcloneLock())
	if err != nil {
		return "", err
	}
	defer unlock()
	dir := d.Paths.RcloneDir()
	if err := fetch.RemoveStaleTemps(dir, tempPattern); err != nil {
		return "", err
	}
	path := d.Paths.RcloneBinary(r.version)
	switch ok, err := fetch.Matches(path, r.binarySHA256, maxBinaryBytes); {
	case err != nil:
		return "", err
	case ok:
		return path, nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	if err := os.Chmod(dir, 0o755); err != nil {
		return "", err
	}
	d.Logf("Downloading rclone %s from %s", r.version, r.url)
	archive, err := os.CreateTemp(dir, tempPattern)
	if err != nil {
		return "", err
	}
	defer os.Remove(archive.Name())
	sum, err := fetch.Download(ctx, fetch.HTTPSOnly(d.HTTPClient()), r.url, archive, maxZipBytes)
	if cerr := archive.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", err
	}
	if sum != r.zipSHA256 {
		return "", fmt.Errorf("%s has SHA-256 %s, not the pinned %s", r.url, sum, r.zipSHA256)
	}
	bin, err := os.CreateTemp(dir, tempPattern)
	if err != nil {
		return "", err
	}
	defer os.Remove(bin.Name()) // fails harmlessly once renamed
	sum, err = extract(archive.Name(), r.member, bin)
	if err == nil {
		err = bin.Sync()
	}
	if cerr := bin.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", err
	}
	if sum != r.binarySHA256 {
		return "", fmt.Errorf("%s in %s has SHA-256 %s, not the pinned %s", r.member, r.url, sum, r.binarySHA256)
	}
	if err := os.Chmod(bin.Name(), 0o755); err != nil {
		return "", err
	}
	if err := os.Rename(bin.Name(), path); err != nil {
		return "", err
	}
	return path, nil
}

// extract copies the zip's regular file member to w and returns its SHA-256.
// Only that one name is read, and it is written to w, never to a path the
// zip names.
func extract(archive, member string, w io.Writer) (string, error) {
	z, err := zip.OpenReader(archive)
	if err != nil {
		return "", err
	}
	defer z.Close()
	for _, f := range z.File {
		if f.Name != member || !f.Mode().IsRegular() {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return "", err
		}
		defer rc.Close()
		sum, _, err := fetch.Copy(w, rc, maxBinaryBytes)
		return sum, err
	}
	return "", fmt.Errorf("%s has no file %s", archive, member)
}
