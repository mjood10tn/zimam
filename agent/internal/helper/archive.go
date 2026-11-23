package helper

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"strings"
	"syscall"
)

// archiveTree writes a gzip tarball of the named paths inside root to w,
// for a backup's files part (R05-D4). It runs as the site user, so root
// never reads the home (D5). Names in the archive are the paths as given,
// relative to the home, which extract-tar unpacks under a restore folder.
//
// A symlink is stored as a link and never followed, wherever it points, so
// the archive holds nothing from outside the home; a top path that is a
// link is stored as one too. The agent names the active release by the
// target stat gives for site/current, so the release's relative links keep
// their depth. Fifos, sockets and devices are skipped: reading one could
// block or read another process's data.
func archiveTree(root *os.Root, paths []string, w io.Writer) error {
	gz := gzip.NewWriter(w)
	tw := tar.NewWriter(gz)
	for _, p := range paths {
		if err := archivePath(p); err != nil {
			return err
		}
		if err := archiveOne(root, tw, p); err != nil {
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	return gz.Close()
}

// archivePath checks a top path: relative, clean, below the home, without
// "..".
func archivePath(p string) error {
	if p == "" || p == "." || p == ".." || path.IsAbs(p) || path.Clean(p) != p || strings.HasPrefix(p, "../") ||
		strings.ContainsAny(p, "\x00\r\n\\") {
		return usageError{fmt.Errorf("archive-tree: %q is not a clean path inside the home", p)}
	}
	return nil
}

func archiveOne(root *os.Root, tw *tar.Writer, name string) error {
	fi, err := root.Lstat(name)
	if err != nil {
		return err
	}
	if fi.Mode()&fs.ModeSymlink != 0 {
		return writeLink(root, tw, name, fi)
	}
	// WalkDir descends only into entries ReadDir reports as directories, so
	// it never walks into a link.
	return fs.WalkDir(root.FS(), name, func(p string, _ fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		fi, err := root.Lstat(p)
		if err != nil {
			return err
		}
		switch {
		case fi.Mode()&fs.ModeSymlink != 0:
			return writeLink(root, tw, p, fi)
		case fi.IsDir():
			return writeHeader(tw, p+"/", fi, "")
		case fi.Mode().IsRegular():
			return archiveFile(root, tw, p)
		}
		return nil // a fifo, socket or device
	})
}

func writeHeader(tw *tar.Writer, name string, fi fs.FileInfo, link string) error {
	h, err := tar.FileInfoHeader(fi, link)
	if err != nil {
		return err
	}
	// Owners don't travel: a restore makes everything the site user's.
	h.Name, h.Uid, h.Gid, h.Uname, h.Gname = name, 0, 0, "", ""
	return tw.WriteHeader(h)
}

func writeLink(root *os.Root, tw *tar.Writer, name string, fi fs.FileInfo) error {
	target, err := root.Readlink(name)
	if err != nil {
		return err
	}
	return writeHeader(tw, name, fi, target)
}

// archiveFile stores a regular file, opened without following a link that
// replaced it since the walk saw it, with the size it had when opened.
func archiveFile(root *os.Root, tw *tar.Writer, name string) error {
	f, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() {
		return nil // replaced by a fifo or device since the walk
	}
	if err := writeHeader(tw, name, fi, ""); err != nil {
		return err
	}
	if _, err := io.CopyN(tw, f, fi.Size()); err != nil {
		if errors.Is(err, io.EOF) {
			return fmt.Errorf("%s shrank while it was archived", name)
		}
		return err
	}
	return nil
}
