package helper

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
)

// entry is one member of an archive as the test sees it.
type entry struct {
	typ  byte
	link string
	body string
}

// members reads a gzip tarball into its members by name.
func members(t *testing.T, archive []byte) map[string]entry {
	t.Helper()
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	out := map[string]entry{}
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(tr)
		out[h.Name] = entry{typ: h.Typeflag, link: h.Linkname, body: string(body)}
	}
}

// site is a Laravel-shaped home: a release, current pointing at it, and
// shared files the release links to.
func site(t *testing.T) (*os.Root, string, string) {
	t.Helper()
	root, home, outside := testHome(t)
	put(t, home, "site/releases/r1/public/index.php", "<?php echo 1;")
	put(t, home, "site/releases/r1/artisan", "#!/usr/bin/env php")
	os.Chmod(filepath.Join(home, "site/releases/r1/artisan"), 0o750)
	put(t, home, "site/shared/.env", "APP_KEY=base64:x")
	put(t, home, "site/shared/storage/app/upload.txt", "uploaded")
	put(t, home, ".ssh/zimam_deploy", "PRIVATE KEY")
	os.Symlink("../../shared/.env", filepath.Join(home, "site/releases/r1/.env"))
	os.Symlink("releases/r1", filepath.Join(home, "site/current"))
	return root, home, outside
}

func archive(t *testing.T, root *os.Root, paths ...string) ([]byte, error) {
	t.Helper()
	var buf bytes.Buffer
	err := archiveTree(root, paths, &buf)
	return buf.Bytes(), err
}

func TestArchiveRoundTripsThroughExtract(t *testing.T) {
	root, home, _ := site(t)
	out, err := archive(t, root, "site/shared", "site/releases/r1")
	if err != nil {
		t.Fatal(err)
	}
	m := members(t, out)
	if m["site/shared/.env"].body != "APP_KEY=base64:x" || m["site/releases/r1/.env"].link != "../../shared/.env" {
		t.Errorf("members: %v", m)
	}
	for name := range m {
		if strings.HasPrefix(name, ".ssh") {
			t.Errorf("archived %s, which wasn't named", name)
		}
	}
	os.Mkdir(filepath.Join(home, "restores"), 0o750)
	if err := extractTarGz(root, "restores", bytes.NewReader(out), testLimits); err != nil {
		t.Fatal(err)
	}
	for rel, want := range map[string]string{
		"restores/site/shared/.env":                   "APP_KEY=base64:x",
		"restores/site/shared/storage/app/upload.txt": "uploaded",
		"restores/site/releases/r1/public/index.php":  "<?php echo 1;",
		"restores/site/releases/r1/.env":              "APP_KEY=base64:x", // through the restored link
	} {
		if b, err := os.ReadFile(filepath.Join(home, rel)); err != nil || string(b) != want {
			t.Errorf("%s = %q, %v", rel, b, err)
		}
	}
	if fi, _ := os.Stat(filepath.Join(home, "restores/site/releases/r1/artisan")); fi == nil || fi.Mode().Perm() != 0o750 {
		t.Errorf("artisan lost its execute bit: %v", fi)
	}
}

// A link is stored as a link and never followed, wherever it points: the
// archive holds no byte of its target (§5.6, D5).
func TestArchiveStoresLinksWithoutFollowing(t *testing.T) {
	root, home, outside := site(t)
	put(t, outside, "shadow", "root:secret-hash")
	os.Symlink(filepath.Join(outside, "shadow"), filepath.Join(home, "site/shared/evil"))
	os.Remove(filepath.Join(home, "site/shared/storage/app/upload.txt"))
	os.Symlink(outside, filepath.Join(home, "site/shared/storage/app/dir-link"))
	// A top path that is itself a link is stored as one, too.
	os.Symlink(outside, filepath.Join(home, "site/top-link"))
	out, err := archive(t, root, "site/shared", "site/top-link")
	if err != nil {
		t.Fatal(err)
	}
	m := members(t, out)
	if e := m["site/shared/evil"]; e.typ != tar.TypeSymlink || e.link != filepath.Join(outside, "shadow") {
		t.Errorf("evil = %+v", e)
	}
	if e := m["site/top-link"]; e.typ != tar.TypeSymlink {
		t.Errorf("top-link = %+v", e)
	}
	raw, _ := gunzip(out)
	if bytes.Contains(raw, []byte("secret-hash")) {
		t.Error("the archive holds the link target's content")
	}
	for name := range m {
		if strings.HasPrefix(name, "site/shared/storage/app/dir-link/") {
			t.Errorf("walked into the link: %s", name)
		}
	}
}

func gunzip(b []byte) ([]byte, error) {
	gz, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		return nil, err
	}
	return io.ReadAll(gz)
}

func TestArchiveSkipsDevicesAndFifos(t *testing.T) {
	root, home, _ := site(t)
	if err := syscall.Mkfifo(filepath.Join(home, "site/shared/pipe"), 0o640); err != nil {
		t.Fatal(err)
	}
	// A fifo would block a reader forever; it is skipped, never opened.
	out, err := archive(t, root, "site/shared")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := members(t, out)["site/shared/pipe"]; ok {
		t.Error("archived the fifo")
	}
}

func TestArchiveRefusesPathOutsideRoot(t *testing.T) {
	root, home, outside := site(t)
	os.Symlink(outside, filepath.Join(home, "escape"))
	for _, p := range []string{"../outside", "/etc", "", ".", "site/../../outside", "site//shared", "escape/", "site/shared\n"} {
		if out, err := archive(t, root, p); err == nil {
			t.Errorf("%q: archived %d bytes", p, len(out))
		}
	}
}

// The agent finds the active release with stat on site/current and names
// that folder, so the release's relative links keep their depth and still
// resolve after a restore. current itself is only ever stored as a link,
// and a current pointing out of the home gives no path to archive.
func TestArchiveOfCurrentResolvesInsideHome(t *testing.T) {
	root, home, outside := site(t)
	cur, err := stat(root, "site/current")
	if err != nil || cur.Target != "releases/r1" {
		t.Fatalf("stat(current) = %+v, %v", cur, err)
	}
	out, err := archive(t, root, "site/shared", path.Join("site", cur.Target), "site/current")
	if err != nil {
		t.Fatal(err)
	}
	m := members(t, out)
	if m["site/releases/r1/public/index.php"].body != "<?php echo 1;" || m["site/current"].link != "releases/r1" {
		t.Errorf("members: %v", m)
	}
	os.MkdirAll(filepath.Join(home, "restores/x"), 0o750)
	if err := extractTarGz(root, "restores/x", bytes.NewReader(out), testLimits); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(home, "restores/x/site/current/.env")); err != nil {
		t.Errorf("the restored release's .env link doesn't resolve: %q, %v", b, err)
	}
	os.Remove(filepath.Join(home, "site/current"))
	os.Symlink(outside, filepath.Join(home, "site/current"))
	cur, _ = stat(root, "site/current")
	if _, err := archive(t, root, path.Join("site", cur.Target)); err == nil {
		t.Error("archived a current that points out of the home")
	}
}

func TestArchiveServeStreamsWithoutResultLine(t *testing.T) {
	root, _, _ := site(t)
	var stdout, stderr bytes.Buffer
	if code := Serve(root, "archive-tree", []string{"site/shared"}, nil, &stdout, &stderr); code != exitOK {
		t.Fatalf("exit %d: %s", code, stderr.String())
	}
	if m := members(t, stdout.Bytes()); m["site/shared/.env"].body == "" {
		t.Errorf("stdout isn't the archive: %v", m)
	}
	if code := Serve(root, "archive-tree", nil, nil, &stdout, &stderr); code != exitUsage {
		t.Errorf("no paths: exit %d", code)
	}
}

func TestClientArchiveTree(t *testing.T) {
	c, a := testClient(t)
	ctx := context.Background()
	if err := c.Mkdir(ctx, a, "site/shared", 0o750); err != nil {
		t.Fatal(err)
	}
	if err := c.Write(ctx, a, "site/shared/.env", 0o600, strings.NewReader("APP_KEY=x")); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := c.ArchiveTree(ctx, a, &buf, "site/shared"); err != nil {
		t.Fatal(err)
	}
	if m := members(t, buf.Bytes()); m["site/shared/.env"].body != "APP_KEY=x" {
		t.Errorf("members: %v", m)
	}
	if err := c.ArchiveTree(ctx, a, io.Discard, "nope"); err == nil {
		t.Error("archived a missing path")
	}
}

func FuzzArchivePath(f *testing.F) {
	for _, s := range []string{"site/shared", "site/current/", "../x", "/etc", "a/../b", "a//b", ".", "", "a/./b", "a\x00b", `a\b`, "..", "site/"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		if archivePath(s) != nil {
			return
		}
		if s == "" || s == "." || strings.HasPrefix(s, "/") || filepath.Clean(s) != s ||
			slices.Contains(strings.Split(s, "/"), "..") || strings.ContainsAny(s, "\x00\r\n\\") {
			t.Errorf("accepted %q", s)
		}
	})
}
