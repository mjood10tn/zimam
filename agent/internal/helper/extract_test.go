package helper

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"io/fs"
	"math"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"testing"
)

// member is one entry of a test tarball.
type member struct {
	name, body, link string
	typ              byte
	mode             int64
}

func tarFile(name, body string) member {
	return member{name: name, body: body, typ: tar.TypeReg, mode: 0o644}
}

func tarDir(name string) member { return member{name: name, typ: tar.TypeDir, mode: 0o755} }

func tarLink(name, target string) member {
	return member{name: name, link: target, typ: tar.TypeSymlink, mode: 0o777}
}

// tarball returns a gzip tarball of ms, in order.
func tarball(t *testing.T, ms ...member) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, m := range ms {
		h := &tar.Header{Name: m.name, Linkname: m.link, Typeflag: m.typ, Mode: m.mode, Size: int64(len(m.body))}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(m.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return &buf
}

var testLimits = ExtractLimits{MaxBytes: 1 << 20, MaxEntries: 100}

// extractInto unpacks a tarball of ms into rel/ inside a test home.
func extractInto(t *testing.T, lim ExtractLimits, ms ...member) (home, outside string, err error) {
	t.Helper()
	root, home, outside := testHome(t)
	if err := os.Mkdir(filepath.Join(home, "rel"), 0o750); err != nil {
		t.Fatal(err)
	}
	return home, outside, extractTarGz(root, "rel", tarball(t, ms...), lim)
}

func TestExtractUnpacks(t *testing.T) {
	home, _, err := extractInto(t, testLimits,
		tarDir("./"),
		tarDir("app/"),
		tarFile("app/index.php", "<?php echo 1;"),
		tarDir("app/public/"),
		tarLink("app/public/index.php", "../index.php"),
		tarFile("README", "hello"),
	)
	if err != nil {
		t.Fatal(err)
	}
	rel := filepath.Join(home, "rel")
	if got := readString(t, filepath.Join(rel, "app/public/index.php")); got != "<?php echo 1;" {
		t.Errorf("app/public/index.php holds %q", got)
	}
	if target, err := os.Readlink(filepath.Join(rel, "app/public/index.php")); err != nil || target != "../index.php" {
		t.Errorf("app/public/index.php -> %q, %v; want ../index.php", target, err)
	}
	if got := readString(t, filepath.Join(rel, "README")); got != "hello" {
		t.Errorf("README holds %q", got)
	}
}

func TestExtractRejectsAbsolute(t *testing.T) {
	outsiders := []string{"/tmp/zimam-extract-test", "/tmp/zimam-extract-test-dir", "/tmp/zimam-extract-test-link"}
	for _, m := range []member{tarFile(outsiders[0], "x"), tarDir(outsiders[1] + "/"), tarLink(outsiders[2], "x")} {
		home, _, err := extractInto(t, testLimits, m)
		if err == nil {
			t.Errorf("%s: extracted, want an error", m.name)
		}
		if ns := names(t, filepath.Join(home, "rel")); len(ns) != 0 {
			t.Errorf("%s: rel/ holds %q", m.name, ns)
		}
	}
	for _, p := range outsiders {
		if _, err := os.Lstat(p); err == nil {
			t.Errorf("%s was created", p)
			os.RemoveAll(p)
		}
	}
}

func TestExtractRejectsDotDot(t *testing.T) {
	for _, name := range []string{"../../outside/x", "a/../../../outside/x", "a/../b", ".."} {
		home, outside, err := extractInto(t, testLimits, tarDir("a/"), tarFile(name, "x"))
		if err == nil {
			t.Errorf("%s: extracted, want an error", name)
		}
		if ns := names(t, outside); len(ns) != 0 {
			t.Errorf("%s: outside holds %q", name, ns)
		}
		gone(t, filepath.Join(home, "rel/b"))
	}
}

func TestExtractRejectsEscapingSymlink(t *testing.T) {
	for _, m := range []member{
		tarLink("l", "../../outside"),
		tarLink("a/l", "../../../outside"),
		tarLink("l", ".."), // the home, outside the release
		tarLink("l", "/etc/passwd"),
	} {
		home, _, err := extractInto(t, testLimits, tarDir("a/"), m)
		if err == nil {
			t.Errorf("%s -> %s: extracted, want an error", m.name, m.link)
		}
		gone(t, filepath.Join(home, "rel", m.name))
	}
}

func TestExtractRejectsLinkThroughLink(t *testing.T) {
	// d -> "." makes d/d/e the same file as e. Seen from d/d/e, the target
	// ../../outside ends at "outside", inside the archive; from e, where the
	// link really lands, it leaves the home.
	home, _, err := extractInto(t, testLimits, tarLink("d", "."), tarLink("d/d/e", "../../outside"))
	if err == nil {
		t.Error("a link under a link: extracted, want an error")
	}
	gone(t, filepath.Join(home, "rel/e"))
	// A file under a link member is refused the same way.
	home, _, err = extractInto(t, testLimits, tarDir("sub/"), tarLink("d", "sub"), tarFile("d/x", "y"))
	if err == nil {
		t.Error("a file under a link: extracted, want an error")
	}
	gone(t, filepath.Join(home, "rel/sub/x"))
}

func TestExtractRejectsHardLink(t *testing.T) {
	home, _, err := extractInto(t, testLimits, tarFile("f", "x"), member{name: "h", link: "f", typ: tar.TypeLink})
	if err == nil {
		t.Error("a hard link: extracted, want an error")
	}
	gone(t, filepath.Join(home, "rel/h"))
}

func TestExtractRejectsDevice(t *testing.T) {
	for _, typ := range []byte{tar.TypeChar, tar.TypeBlock, tar.TypeFifo} {
		home, _, err := extractInto(t, testLimits, member{name: "dev", typ: typ, mode: 0o666})
		if err == nil {
			t.Errorf("type %q: extracted, want an error", typ)
		}
		gone(t, filepath.Join(home, "rel/dev"))
	}
}

// overflowingTarball holds a 1-byte file, then an entry whose header claims
// math.MaxInt64 bytes but holds 3: added to the first, the size overflows.
func overflowingTarball(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: "a", Typeflag: tar.TypeReg, Mode: 0o644, Size: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	if err := tw.WriteHeader(&tar.Header{Name: "b", Typeflag: tar.TypeReg, Mode: 0o644, Size: math.MaxInt64}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte("abc")); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil { // the archive ends inside b
		t.Fatal(err)
	}
	return &buf
}

func TestExtractSizeLimit(t *testing.T) {
	lim := ExtractLimits{MaxBytes: 10, MaxEntries: 100}
	if _, _, err := extractInto(t, lim, tarFile("a", "123456"), tarFile("b", "1234")); err != nil {
		t.Errorf("exactly 10 bytes: %v", err)
	}
	home, _, err := extractInto(t, lim, tarFile("a", "123456"), tarFile("b", "12345"))
	if err == nil || !strings.Contains(err.Error(), "too large") {
		t.Errorf("11 bytes: %v, want too large", err)
	}
	gone(t, filepath.Join(home, "rel/b"))
	root, home, _ := testHome(t)
	if err := os.Mkdir(filepath.Join(home, "rel"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := extractTarGz(root, "rel", overflowingTarball(t), lim); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Errorf("an entry of math.MaxInt64 bytes: %v, want too large", err)
	}
	gone(t, filepath.Join(home, "rel/b"))
}

func TestExtractEntryLimit(t *testing.T) {
	lim := ExtractLimits{MaxBytes: 1 << 20, MaxEntries: 2}
	if _, _, err := extractInto(t, lim, tarFile("a", "x"), tarFile("b", "y")); err != nil {
		t.Errorf("2 entries: %v", err)
	}
	home, _, err := extractInto(t, lim, tarFile("a", "x"), tarFile("b", "y"), tarFile("c", "z"))
	if err == nil || !strings.Contains(err.Error(), "too many entries") {
		t.Errorf("3 entries: %v, want too many entries", err)
	}
	gone(t, filepath.Join(home, "rel/c"))
}

func TestExtractModes(t *testing.T) {
	// The archive's own modes are ignored, except for a file's execute bits
	// (TestExtractExecutableBit): setuid, setgid and sticky never come through.
	home, _, err := extractInto(t, testLimits,
		member{name: "d/", typ: tar.TypeDir, mode: 0o777},
		member{name: "d/f", body: "x", typ: tar.TypeReg, mode: 0o6666},
		member{name: "d/x", body: "x", typ: tar.TypeReg, mode: 0o7777},
	)
	if err != nil {
		t.Fatal(err)
	}
	if got := modeOf(t, filepath.Join(home, "rel/d")); got != fs.ModeDir|0o750 {
		t.Errorf("directory mode %v, want 0750", got)
	}
	if got := modeOf(t, filepath.Join(home, "rel/d/f")); got != 0o640 {
		t.Errorf("file mode %v, want 0640", got)
	}
	if got := modeOf(t, filepath.Join(home, "rel/d/x")); got != 0o750 {
		t.Errorf("executable file mode %v, want 0750", got)
	}
}

// escapes walks a slash-separated path from the destination and reports
// whether it is absolute or a ".." climbs above its start.
func escapes(p string) bool {
	if strings.HasPrefix(p, "/") {
		return true
	}
	depth := 0
	for _, part := range strings.Split(p, "/") {
		switch part {
		case "", ".":
		case "..":
			if depth == 0 {
				return true
			}
			depth--
		default:
			depth++
		}
	}
	return false
}

// leaves reports whether a cleaned path starts with "/" or a ".." element.
// "..foo" is an ordinary name.
func leaves(p string) bool {
	return strings.HasPrefix(p, "/") || p == ".." || strings.HasPrefix(p, "../")
}

func FuzzMemberName(f *testing.F) {
	for _, s := range []string{"a", "./a/b", "a/b/", "/etc/passwd", "../x", "a/../../x", "a/../b", "..foo", `a\b`, "", ".", "a//b"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, name string) {
		got, err := memberName(name)
		if err != nil {
			return
		}
		if got == "" || leaves(got) || escapes(got) || escapes(name) {
			t.Errorf("memberName(%q) = %q: accepted, but it leaves the destination", name, got)
		}
	})
}

func FuzzLinkStaysInside(f *testing.F) {
	for _, s := range [][2]string{
		{"a", "b"}, {"a/b", "../c"}, {"a", "../x"}, {"a", "/etc"}, {"a/b/c", "../../.."}, {"d", "."}, {"a", ""}, {"a/b", "c/../../.."},
	} {
		f.Add(s[0], s[1])
	}
	f.Fuzz(func(t *testing.T, member, target string) {
		name, err := memberName(member)
		if err != nil || linkStaysInside(name, target) != nil {
			return
		}
		// The link sits in name's directory; its target is walked from there.
		if path.IsAbs(target) || leaves(path.Join(path.Dir(name), target)) || escapes(path.Dir(name)+"/"+target) {
			t.Errorf("linkStaysInside(%q, %q): accepted, but the target leaves the archive", name, target)
		}
	})
}

// TestExtractExecutableBit: a regular file whose tar mode has any execute
// bit extracts as 0750, so a repository's scripts still run; the others stay
// 0640 (spike amendment A4). Other bits never come through.
func TestExtractExecutableBit(t *testing.T) {
	home, _, err := extractInto(t, testLimits,
		member{name: "run.sh", body: "#!/bin/sh\n", typ: tar.TypeReg, mode: 0o755},
		member{name: "group-x", body: "x", typ: tar.TypeReg, mode: 0o610},
		member{name: "other-x", body: "x", typ: tar.TypeReg, mode: 0o4601},
		member{name: "plain", body: "x", typ: tar.TypeReg, mode: 0o666},
	)
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]fs.FileMode{"run.sh": 0o750, "group-x": 0o750, "other-x": 0o750, "plain": 0o640} {
		if got := modeOf(t, filepath.Join(home, "rel", name)); got != want {
			t.Errorf("%s mode %v, want %v", name, got, want)
		}
	}
}

// gitRepo makes a repository with git in a temporary directory, hermetic:
// no system or global config, a fixed author. It skips the test without
// /usr/bin/git.
func gitRepo(t *testing.T) (dir string, git func(args ...string) string) {
	t.Helper()
	if _, err := os.Stat("/usr/bin/git"); err != nil {
		t.Skip("needs /usr/bin/git")
	}
	dir = t.TempDir()
	env := []string{
		"PATH=/usr/bin:/bin", "HOME=" + dir, "LANG=C.UTF-8",
		"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com",
	}
	git = func(args ...string) string {
		t.Helper()
		cmd := exec.Command("/usr/bin/git", args...)
		cmd.Dir, cmd.Env = dir, env
		out, err := cmd.Output()
		if err != nil {
			var stderr []byte
			if ee, ok := err.(*exec.ExitError); ok {
				stderr = ee.Stderr
			}
			t.Fatalf("git %q: %v\n%s", args, err, stderr)
		}
		return string(out)
	}
	git("init", "-q", "-b", "main")
	return dir, git
}

// TestExtractRealGitArchive feeds extract-tar what git archive really
// prints (spike amendment A1): a tree's archive extracts, with the script's
// execute bit and a symlink inside; a commit's archive carries a
// pax_global_header and is refused.
func TestExtractRealGitArchive(t *testing.T) {
	dir, git := gitRepo(t)
	put(t, dir, "public/index.php", "<?php echo 1;\n")
	put(t, dir, "bin/deploy.sh", "#!/bin/sh\n")
	if err := os.Chmod(filepath.Join(dir, "bin/deploy.sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	symlinkAt(t, "public/index.php", filepath.Join(dir, "index.php"))
	git("add", "-A")
	git("commit", "-q", "-m", "one")

	root, home, _ := testHome(t)
	for _, d := range []string{"rel", "rel2"} {
		if err := root.Mkdir(d, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	tree := git("archive", "--format=tar.gz", "HEAD^{tree}")
	if err := extractTarGz(root, "rel", strings.NewReader(tree), testLimits); err != nil {
		t.Fatalf("extracting a tree's archive: %v", err)
	}
	if got := readString(t, filepath.Join(home, "rel/public/index.php")); got != "<?php echo 1;\n" {
		t.Errorf("index.php = %q", got)
	}
	if got := modeOf(t, filepath.Join(home, "rel/bin/deploy.sh")); got != 0o750 {
		t.Errorf("deploy.sh mode %v, want 0750", got)
	}
	if got := modeOf(t, filepath.Join(home, "rel/public/index.php")); got != 0o640 {
		t.Errorf("index.php mode %v, want 0640", got)
	}
	if got, err := os.Readlink(filepath.Join(home, "rel/index.php")); err != nil || got != "public/index.php" {
		t.Errorf("index.php link -> %q, %v", got, err)
	}

	commit := git("archive", "--format=tar.gz", "HEAD")
	err := extractTarGz(root, "rel2", strings.NewReader(commit), testLimits)
	if err == nil || !strings.Contains(err.Error(), "pax_global_header") {
		t.Errorf("extracting a commit's archive: %v; want the pax_global_header refused", err)
	}
}

// A target that passes through another link member reads as inside
// and lands outside. With a/b/c/d -> ../.. (which is a), the target
// d/../../../secret.txt is a/secret.txt as text, and three levels above a
// on disk. It is refused whichever of the two links comes first.
func TestExtractRejectsTargetThroughLink(t *testing.T) {
	d := tarLink("a/b/c/d", "../..")
	e := tarLink("a/b/c/e", "d/../../../secret.txt")
	for name, ms := range map[string][]member{
		"the link first":   {tarDir("a/b/c/"), d, e},
		"the target first": {tarDir("a/b/c/"), e, d},
	} {
		home, _, err := extractInto(t, testLimits, ms...)
		if err == nil || !strings.Contains(err.Error(), "through the symlink") {
			t.Errorf("%s: err = %v, want a refusal naming the link it goes through", name, err)
		}
		// Whatever was made before the refusal, nothing reads past the release.
		if err := os.WriteFile(filepath.Join(home, "secret.txt"), []byte("secret"), 0o600); err != nil {
			t.Fatal(err)
		}
		if got, err := os.ReadFile(filepath.Join(home, "rel/a/b/c/e")); err == nil && name == "the link first" {
			t.Errorf("%s: e was made and reads %q", name, got)
		}
	}
	// A link to a link, and a link beside one, are fine.
	if _, _, err := extractInto(t, testLimits, tarDir("a/"), tarFile("a/f", "x"), tarLink("a/d", "f"), tarLink("a/e", "d"), tarLink("g", "a/f")); err != nil {
		t.Errorf("a link to a link: %v", err)
	}
}

func TestLinkAvoidsLinks(t *testing.T) {
	links := map[string]string{"a/d": "..", "l": "a"}
	for _, c := range []struct {
		member, target string
		ok             bool
	}{
		{"a/e", "d", true},         // ends at a link
		{"a/e", "d/x", false},      // goes through one
		{"a/e", "./d/../x", false}, // even to come back
		{"a/e", "../l/f", false},   // by way of ..
		{"a/e", "../l", true},      // ends at one
		{"a/e", "dd/x", true},      // a name that only starts like one
		{"a/b/e", "../d/", false},  // a trailing slash follows the link
		{"x", "a/f", true},
	} {
		if err := linkAvoidsLinks(c.member, c.target, links); (err == nil) != c.ok {
			t.Errorf("linkAvoidsLinks(%q, %q) = %v, want ok %t", c.member, c.target, err, c.ok)
		}
	}
}
