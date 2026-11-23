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
// archive. Directories are 0750 and files 0640, or 0750 with an execute
// bit (ExtractedFileMode). Absolute paths, "..", hard links and device
// files are refused, and os.Root contains anything the checks miss.
//
// linkStaysInside reads a target from where the link's name says it is, so
// a member under an earlier symlink member is refused: through d -> ".",
// the link d/d/e lands at e, and ../../x is checked from the wrong place.
// For the same reason a link's target may not pass through another link
// member (linkAvoidsLinks): with d -> "../..", the target d/../../../x reads
// as inside and lands outside. Each link is checked against the links
// before it when it is made, and all of them against the whole set at the
// end, since a later member can become a link on an earlier target's way.
func extractTarGz(root *os.Root, dest string, r io.Reader, lim ExtractLimits) error {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	var total int64
	links := map[string]string{} // the symlink members so far, and their targets
	for n := 0; ; n++ {
		h, err := tr.Next()
		if err == io.EOF {
			for name, target := range links {
				if err := linkAvoidsLinks(name, target, links); err != nil {
					return err
				}
			}
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
			if _, ok := links[dir]; ok {
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
			f, err := root.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, ExtractedFileMode(h.Mode))
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
			if err := linkAvoidsLinks(name, h.Linkname, links); err != nil {
				return err
			}
			if err := root.Symlink(h.Linkname, target); err != nil {
				return err
			}
			links[name] = h.Linkname
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

// LinkRefused says why extract-tar would refuse the symlink member with
// target, given every symlink member of the archive (links), or nil when
// it would make it. A files restore drops such links before extracting
// (R05 Task 8).
func LinkRefused(member, target string, links map[string]string) error {
	if err := linkStaysInside(member, target); err != nil {
		return err
	}
	return linkAvoidsLinks(member, target, links)
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

// linkAvoidsLinks walks target from the link's folder one component at a
// time, as the kernel will, and refuses one that steps through a symlink
// member: from there on the text and the disk disagree about where the
// path is. The last component may itself be a link, which is then checked
// in its own right. It assumes linkStaysInside passed.
func linkAvoidsLinks(member, target string, links map[string]string) error {
	parts := strings.Split(target, "/")
	at := path.Dir(member)
	for i, part := range parts {
		switch part {
		case "", ".":
			continue
		case "..":
			at = path.Dir(at)
			continue
		}
		at = path.Join(at, part)
		// Only what the path goes through counts: anything but the end.
		if _, ok := links[at]; ok && i < len(parts)-1 && at != member {
			return fmt.Errorf("symlink %q points through the symlink %q", member, at)
		}
	}
	return nil
}

// ExtractedFileMode is what a regular file extracts as (spike amendment
// A4): 0750 when its tar mode has any execute bit, so a repository's
// scripts still run, and 0640 otherwise. No other bit of the archive's
// mode comes through.
func ExtractedFileMode(tarMode int64) os.FileMode {
	if tarMode&0o111 != 0 {
		return 0o750
	}
	return 0o640
}
