package helper

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"strings"
)

type ExtractLimits struct {
	MaxBytes   int64 // sum of regular file sizes
	MaxEntries int
}

// extractTarGz unpacks a gzip tarball into dest inside root. It allows only
// directories, regular files and relative symlinks that stay inside the
// archive. Absolute paths, "..", hard links and device files are refused,
// and os.Root contains anything the checks miss.
//
// linkStaysInside reads a target from where the link's name says it is, so
// a member under an earlier symlink member is refused: through d -> ".",
// the link d/d/e lands at e, and ../../x is checked from the wrong place.
func extractTarGz(root *os.Root, dest string, r io.Reader, lim ExtractLimits) error {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	var total int64
	links := map[string]bool{} // the symlink members so far
	for n := 0; ; n++ {
		h, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		if n >= lim.MaxEntries {
			return errors.New("archive has too many entries")
		}
		name, err := memberName(h.Name)
		if err != nil {
			return err
		}
		for dir := path.Dir(name); dir != "."; dir = path.Dir(dir) {
			if links[dir] {
				return fmt.Errorf("archive member %q is inside the symlink %q", h.Name, dir)
			}
		}
		target := path.Join(dest, name)
		switch h.Typeflag {
		case tar.TypeDir:
			if err := root.MkdirAll(target, 0o750); err != nil {
				return err
			}
		case tar.TypeReg:
			if h.Size < 0 || h.Size > lim.MaxBytes-total { // total+h.Size could overflow
				return errors.New("archive is too large")
			}
			total += h.Size
			f, err := root.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o640)
			if err != nil {
				return err
			}
			_, err = io.CopyN(f, tr, h.Size)
			if cerr := f.Close(); err == nil {
				err = cerr
			}
			if err != nil {
				return err
			}
		case tar.TypeSymlink:
			if err := linkStaysInside(name, h.Linkname); err != nil {
				return err
			}
			if err := root.Symlink(h.Linkname, target); err != nil {
				return err
			}
			links[name] = true
		default:
			return fmt.Errorf("archive member %q has unsupported type %q", h.Name, h.Typeflag)
		}
	}
}

func memberName(name string) (string, error) {
	if name == "" || strings.HasPrefix(name, "/") || strings.Contains(name, `\`) {
		return "", fmt.Errorf("archive member %q has an unsafe path", name)
	}
	for _, part := range strings.Split(name, "/") {
		if part == ".." {
			return "", fmt.Errorf("archive member %q has an unsafe path", name)
		}
	}
	return path.Clean(name), nil
}

func linkStaysInside(member, target string) error {
	if path.IsAbs(target) {
		return fmt.Errorf("symlink %q points outside the archive", member)
	}
	resolved := path.Join(path.Dir(member), target)
	if resolved == ".." || strings.HasPrefix(resolved, "../") {
		return fmt.Errorf("symlink %q points outside the archive", member)
	}
	return nil
}
