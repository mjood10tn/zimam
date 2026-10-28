package helper

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"zimam/agent/contract"
)

// testHome returns a home directory under a temporary directory, opened as
// an os.Root, and a directory beside it that the home must never reach.
func testHome(t *testing.T) (root *os.Root, home, outside string) {
	t.Helper()
	base := t.TempDir()
	home = filepath.Join(base, "home")
	outside = filepath.Join(base, "outside")
	for _, d := range []string{home, outside} {
		if err := os.Mkdir(d, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	root, err := os.OpenRoot(home)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.Close() })
	return root, home, outside
}

// put creates the file rel under dir, with its parents, and returns its path.
func put(t *testing.T, dir, rel, content string) string {
	t.Helper()
	p := filepath.Join(dir, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o640); err != nil {
		t.Fatal(err)
	}
	return p
}

// symlinkAt creates the symlink at -> target, with at's parents.
func symlinkAt(t *testing.T, target, at string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(at), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, at); err != nil {
		t.Fatal(err)
	}
}

func readString(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// names lists the directory dir.
func names(t *testing.T, dir string) []string {
	t.Helper()
	es, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	ns := []string{}
	for _, e := range es {
		ns = append(ns, e.Name())
	}
	return ns
}

func gone(t *testing.T, p string) {
	t.Helper()
	if _, err := os.Lstat(p); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("%s exists (err %v), want it gone", p, err)
	}
}

func modeOf(t *testing.T, p string) fs.FileMode {
	t.Helper()
	fi, err := os.Lstat(p)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Mode()
}

func inode(t *testing.T, p string) uint64 {
	t.Helper()
	fi, err := os.Lstat(p)
	if err != nil {
		t.Fatal(err)
	}
	return fi.Sys().(*syscall.Stat_t).Ino
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("read failed") }

// fixedClock never moves, so no search runs out of time.
func fixedClock() time.Time { return time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC) }

func TestLimits(t *testing.T) {
	if MaxWriteBytes != 16<<20 || MaxReadBytes != 1<<20 || MaxListEntries != 10_000 || MaxScanEntries != 100_000 ||
		MaxTailLines != 1000 || MaxSearchMatches != 2000 || SearchTimeout != 10*time.Second {
		t.Errorf("limits: write %d, read %d, list %d, scan %d, tail %d, search %d in %v; want the brief's",
			MaxWriteBytes, MaxReadBytes, MaxListEntries, MaxScanEntries, MaxTailLines, MaxSearchMatches, SearchTimeout)
	}
}

func TestReadRefusesSymlinkOutside(t *testing.T) {
	root, home, outside := testHome(t)
	put(t, outside, "secret", "outside data")
	symlinkAt(t, "/etc/passwd", filepath.Join(home, "logs/php-error.log"))
	symlinkAt(t, "../../outside/secret", filepath.Join(home, "logs/relative.log"))
	for _, name := range []string{"logs/php-error.log", "logs/relative.log"} {
		res, err := readFile(root, name, 0, 1024)
		if err == nil || len(res.Data) != 0 {
			t.Errorf("readFile(%s) = %q, %v; want an error and no data", name, res.Data, err)
		}
	}
}

func TestReadRefusesDotDot(t *testing.T) {
	root, home, outside := testHome(t)
	put(t, outside, "secret", "outside data")
	if err := os.Mkdir(filepath.Join(home, "logs"), 0o750); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"../outside/secret", "logs/../../outside/secret", "/etc/passwd"} {
		res, err := readFile(root, name, 0, 1024)
		if err == nil || len(res.Data) != 0 {
			t.Errorf("readFile(%s) = %q, %v; want an error and no data", name, res.Data, err)
		}
	}
}

func TestReadOffsetAndLimit(t *testing.T) {
	root, home, _ := testHome(t)
	put(t, home, "f", "0123456789")
	for _, c := range []struct {
		offset, limit int64
		want          string
	}{
		{0, 4, "0123"}, {3, 4, "3456"}, {8, 100, "89"}, {10, 5, ""}, {20, 5, ""},
	} {
		res, err := readFile(root, "f", c.offset, c.limit)
		if err != nil || string(res.Data) != c.want || res.Size != 10 {
			t.Errorf("readFile(f, %d, %d) = %q, size %d, %v; want %q, size 10", c.offset, c.limit, res.Data, res.Size, err, c.want)
		}
	}
	put(t, home, "big", strings.Repeat("x", MaxReadBytes+10))
	if res, err := readFile(root, "big", 0, 2*MaxReadBytes); err != nil || len(res.Data) != MaxReadBytes {
		t.Errorf("a 2 MiB read returned %d bytes, %v; want %d", len(res.Data), err, MaxReadBytes)
	}
	for _, c := range [][2]int64{{-1, 10}, {0, 0}, {0, -5}} {
		if _, err := readFile(root, "f", c[0], c[1]); err == nil {
			t.Errorf("readFile(f, %d, %d): no error", c[0], c[1])
		}
	}
}

func TestReadRefusesNonRegular(t *testing.T) {
	root, home, _ := testHome(t)
	if err := syscall.Mkfifo(filepath.Join(home, "fifo"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(home, "dir"), 0o750); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := readFile(root, "fifo", 0, 10)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Error("reading a FIFO: no error")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("reading a FIFO blocked")
	}
	if _, err := readFile(root, "dir", 0, 10); err == nil {
		t.Error("reading a directory: no error")
	}
}

func TestStatAndList(t *testing.T) {
	root, home, outside := testHome(t)
	put(t, home, "site/releases/one/index.html", "hello")
	symlinkAt(t, "releases/one", filepath.Join(home, "site/current"))
	symlinkAt(t, outside, filepath.Join(home, "site/evil"))

	fi, err := stat(root, "site/releases/one/index.html")
	if err != nil || fi.Name != "index.html" || fi.Size != 5 || fi.Mode != 0o640 || fi.Target != "" || fi.ModTime.IsZero() {
		t.Errorf("stat(index.html) = %+v, %v; want a 5-byte 0640 file", fi, err)
	}
	// Symlinks are reported, never followed, even one that points outside.
	for link, target := range map[string]string{"site/current": "releases/one", "site/evil": outside} {
		fi, err := stat(root, link)
		if err != nil || fi.Mode&fs.ModeSymlink == 0 || fi.Target != target {
			t.Errorf("stat(%s) = %+v, %v; want a symlink to %s", link, fi, err, target)
		}
	}
	if _, err := stat(root, "missing"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("stat(missing): %v, want fs.ErrNotExist", err)
	}

	l, err := list(root, "site", 10)
	var got []string
	for _, e := range l.Entries {
		got = append(got, e.Name)
	}
	if err != nil || l.Truncated || !slices.Equal(got, []string{"current", "evil", "releases"}) {
		t.Errorf("list(site) = %q, truncated %v, %v; want current, evil, releases", got, l.Truncated, err)
	}
	if l, err := list(root, "site", 2); err != nil || len(l.Entries) != 2 || !l.Truncated {
		t.Errorf("list(site, 2) = %d entries, truncated %v, %v; want 2, truncated", len(l.Entries), l.Truncated, err)
	}
	if _, err := list(root, "site/evil", 10); err == nil {
		t.Error("list(site/evil) followed a link outside the home")
	}
}

func TestMkdirMode(t *testing.T) {
	root, home, outside := testHome(t)
	for _, c := range []struct {
		name string
		mode fs.FileMode
	}{
		{"site/releases/initial/public", 0o750},
		{"tmp", 0o700},
		{"tmp", 0o770}, // an existing directory gets the mode too
	} {
		if err := mkdir(root, c.name, c.mode); err != nil {
			t.Fatalf("mkdir(%s, %o): %v", c.name, c.mode, err)
		}
		if got := modeOf(t, filepath.Join(home, c.name)); got != fs.ModeDir|c.mode {
			t.Errorf("mkdir(%s, %o): mode %v", c.name, c.mode, got)
		}
	}
	symlinkAt(t, outside, filepath.Join(home, "evil"))
	if err := mkdir(root, "evil/x", 0o750); err == nil {
		t.Error("mkdir followed a link outside the home")
	}
	if ns := names(t, outside); len(ns) != 0 {
		t.Errorf("outside holds %q", ns)
	}
}

func TestRemoveTreeKeepsOutsideFiles(t *testing.T) {
	for _, target := range []string{".", "site", "site/evil"} {
		t.Run(target, func(t *testing.T) {
			root, home, outside := testHome(t)
			keep := put(t, outside, "keep.txt", "keep")
			put(t, home, "site/data/x.txt", "x")
			symlinkAt(t, outside, filepath.Join(home, "site/evil"))
			symlinkAt(t, "../../outside", filepath.Join(home, "site/up"))
			if err := removeTree(root, target); err != nil {
				t.Fatal(err)
			}
			if got := readString(t, keep); got != "keep" {
				t.Errorf("outside file holds %q", got)
			}
			gone(t, filepath.Join(home, "site/evil"))
		})
	}
}

func TestRemoveTreeKeepsHome(t *testing.T) {
	root, home, _ := testHome(t)
	put(t, home, "site/current/index.html", "x")
	put(t, home, ".bashrc", "x")
	put(t, home, "logs/php-error.log", "x")
	if err := removeTree(root, "."); err != nil {
		t.Fatal(err)
	}
	if !modeOf(t, home).IsDir() {
		t.Fatal("the home is gone")
	}
	if ns := names(t, home); len(ns) != 0 {
		t.Errorf("the home still holds %q", ns)
	}
	// One file, such as an old log archive, goes on its own.
	put(t, home, "logs/old.log.gz", "x")
	put(t, home, "logs/new.log", "y")
	if err := removeTree(root, "logs/old.log.gz"); err != nil {
		t.Fatal(err)
	}
	if ns := names(t, filepath.Join(home, "logs")); !slices.Equal(ns, []string{"new.log"}) {
		t.Errorf("logs/ holds %q, want new.log", ns)
	}
}

func TestWriteIsAtomic(t *testing.T) {
	root, home, _ := testHome(t)
	p := put(t, home, "shared/.env", "old")
	before := inode(t, p)
	check := func(step, want string) {
		t.Helper()
		if got := readString(t, p); got != want {
			t.Errorf("%s: content %q, want %q", step, got, want)
		}
		if ns := names(t, filepath.Join(home, "shared")); !slices.Equal(ns, []string{".env"}) {
			t.Errorf("%s: shared/ holds %q, want only .env", step, ns)
		}
	}
	// A reader that fails part-way leaves the old file and no temporary file.
	if err := writeFile(root, "shared/.env", 0o640, io.MultiReader(strings.NewReader("new"), failingReader{}), 1024); err == nil {
		t.Error("a failing reader: no error")
	}
	check("failing reader", "old")
	// So does content over the limit.
	if err := writeFile(root, "shared/.env", 0o640, strings.NewReader("123456"), 5); err == nil {
		t.Error("content over the limit: no error")
	}
	check("over the limit", "old")
	// Content up to the limit replaces the file through a new inode: the old
	// one is never rewritten in place.
	if err := writeFile(root, "shared/.env", 0o640, strings.NewReader("12345"), 5); err != nil {
		t.Fatal(err)
	}
	check("written", "12345")
	if inode(t, p) == before {
		t.Error("the file was rewritten in place, not renamed over")
	}
	if got := modeOf(t, p); got != 0o640 {
		t.Errorf("mode %v, want 0640", got)
	}
}

func TestWriteReplacesSymlink(t *testing.T) {
	root, home, outside := testHome(t)
	secret := put(t, outside, "shadow", "secret")
	symlinkAt(t, secret, filepath.Join(home, "shared/.env"))
	if err := writeFile(root, "shared/.env", 0o600, strings.NewReader("mine"), 1024); err != nil {
		t.Fatal(err)
	}
	if got := readString(t, secret); got != "secret" {
		t.Errorf("the link's target now holds %q", got)
	}
	p := filepath.Join(home, "shared/.env")
	if !modeOf(t, p).IsRegular() || readString(t, p) != "mine" {
		t.Errorf("shared/.env is %v holding %q, want a regular file with the content", modeOf(t, p), readString(t, p))
	}
}

func TestSymlinkSwapReplacesLink(t *testing.T) {
	root, home, _ := testHome(t)
	for _, d := range []string{"site/releases/one", "site/releases/two"} {
		if err := os.MkdirAll(filepath.Join(home, d), 0o750); err != nil {
			t.Fatal(err)
		}
	}
	current := filepath.Join(home, "site/current")
	for _, target := range []string{"releases/one", "releases/two"} { // creates, then replaces
		if err := symlinkSwap(root, "site/current", target); err != nil {
			t.Fatal(err)
		}
		if got, err := os.Readlink(current); err != nil || got != target {
			t.Errorf("current -> %q, %v; want %s", got, err, target)
		}
	}
	if ns := names(t, filepath.Join(home, "site")); !slices.Equal(ns, []string{"current", "releases"}) {
		t.Errorf("site/ holds %q; a temporary link was left", ns)
	}
}

func TestSymlinkSwapRefusesAbsoluteTarget(t *testing.T) {
	root, home, _ := testHome(t)
	symlinkAt(t, "releases/one", filepath.Join(home, "site/current"))
	for _, target := range []string{"/etc", "/", "/home/s_other/site/releases/one"} {
		if err := symlinkSwap(root, "site/current", target); err == nil {
			t.Errorf("symlinkSwap(%q): no error", target)
		}
	}
	if got, _ := os.Readlink(filepath.Join(home, "site/current")); got != "releases/one" {
		t.Errorf("current -> %q, want it unchanged", got)
	}
	if ns := names(t, filepath.Join(home, "site")); !slices.Equal(ns, []string{"current"}) {
		t.Errorf("site/ holds %q", ns)
	}
}

func TestSymlinkSwapRefusesEscapingTargets(t *testing.T) {
	root, home, outside := testHome(t)
	if err := os.Mkdir(filepath.Join(home, "site"), 0o750); err != nil {
		t.Fatal(err)
	}
	for _, c := range [][2]string{
		{"current", ""},
		{"current", ".."},
		{"current", "../x"},
		{"current", "releases/../.."},
		{"site/current", "../.."},
		{"site/current", "../../outside"},
		{"../current", "releases/one"},
		{"site/../../current", "releases/one"},
		{".", "releases/one"},
	} {
		if err := symlinkSwap(root, c[0], c[1]); err == nil {
			t.Errorf("symlinkSwap(%q, %q): no error", c[0], c[1])
		}
	}
	if ns := names(t, home); !slices.Equal(ns, []string{"site"}) {
		t.Errorf("the home holds %q", ns)
	}
	if ns := names(t, filepath.Join(home, "site")); len(ns) != 0 {
		t.Errorf("site/ holds %q", ns)
	}
	if ns := names(t, outside); len(ns) != 0 {
		t.Errorf("outside holds %q", ns)
	}
}

// A release links its shared files with targets that climb out of the
// release but stay inside the home (design §4 step 6).
func TestSymlinkSwapAcceptsTargetsInsideHome(t *testing.T) {
	root, home, _ := testHome(t)
	if err := os.MkdirAll(filepath.Join(home, "panel/releases/1.0.0/database"), 0o750); err != nil {
		t.Fatal(err)
	}
	for _, c := range [][2]string{
		{"panel/releases/1.0.0/.env", "../../shared/.env"},
		{"panel/releases/1.0.0/storage", "../../shared/storage"},
		{"panel/releases/1.0.0/database/database.sqlite", "../../../shared/database.sqlite"},
		{"panel/current", "releases/1.0.0"},
	} {
		if err := symlinkSwap(root, c[0], c[1]); err != nil {
			t.Errorf("symlinkSwap(%q, %q): %v", c[0], c[1], err)
			continue
		}
		if got, err := os.Readlink(filepath.Join(home, c[0])); err != nil || got != c[1] {
			t.Errorf("%s -> %q, %v; want %s", c[0], got, err, c[1])
		}
	}
}

func TestScanWorldWritableStopsAtLimit(t *testing.T) {
	root, home, _ := testHome(t)
	for i := range 60 {
		put(t, home, fmt.Sprintf("site/d%d/f%d", i%6, i), "x")
	}
	res, err := scanWorldWritable(root, "site", 50)
	if err != nil || !res.Truncated || res.Scanned != 50 {
		t.Errorf("limit 50: scanned %d, truncated %v, %v; want 50, truncated", res.Scanned, res.Truncated, err)
	}
	// site itself, 6 directories and 60 files.
	res, err = scanWorldWritable(root, "site", 1000)
	if err != nil || res.Truncated || res.Scanned != 67 {
		t.Errorf("limit 1000: scanned %d, truncated %v, %v; want 67", res.Scanned, res.Truncated, err)
	}
}

func TestScanWorldWritableFindsWritable(t *testing.T) {
	root, home, _ := testHome(t)
	put(t, home, "site/ok.php", "x")
	upload := put(t, home, "site/public/upload.php", "x")
	cache := filepath.Join(home, "site/cache")
	put(t, home, "site/cache/x", "x")
	for p, mode := range map[string]fs.FileMode{upload: 0o666, cache: 0o777} {
		if err := os.Chmod(p, mode); err != nil {
			t.Fatal(err)
		}
	}
	symlinkAt(t, "/etc", filepath.Join(home, "site/link")) // a link's mode is 0777; it doesn't count
	res, err := scanWorldWritable(root, "site", 1000)
	slices.Sort(res.Paths)
	if err != nil || res.Count != 2 || !slices.Equal(res.Paths, []string{"site/cache", "site/public/upload.php"}) || res.Truncated {
		t.Errorf("scan = %+v, %v; want site/cache and site/public/upload.php", res, err)
	}
}

func TestTail(t *testing.T) {
	root, home, _ := testHome(t)
	put(t, home, "log", "one\ntwo\nthree\nfour")
	for _, c := range []struct {
		before int64
		n      int
		want   []Line
	}{
		{-1, 2, []Line{{Offset: 8, Text: "three"}, {Offset: 14, Text: "four"}}},
		{-1, 10, []Line{{Offset: 0, Text: "one"}, {Offset: 4, Text: "two"}, {Offset: 8, Text: "three"}, {Offset: 14, Text: "four"}}},
		{8, 5, []Line{{Offset: 0, Text: "one"}, {Offset: 4, Text: "two"}}},
		{0, 5, []Line{}},
	} {
		got, err := tail(root, "log", c.before, c.n)
		if err != nil || got.Size != 18 || !slices.Equal(got.Lines, c.want) {
			t.Errorf("tail(log, %d, %d) = %+v, %v; want %+v, size 18", c.before, c.n, got, err, c.want)
		}
	}
	for _, n := range []int{0, MaxTailLines + 1} {
		if _, err := tail(root, "log", -1, n); err == nil {
			t.Errorf("tail of %d lines: no error", n)
		}
	}
}

func TestTailWindow(t *testing.T) {
	root, home, _ := testHome(t)
	// 600 lines of 2000 bytes: the last MiB holds 524 whole lines, and the
	// partial line at the start of the window is left out.
	put(t, home, "big", strings.Repeat(strings.Repeat("a", 1999)+"\n", 600))
	got, err := tail(root, "big", -1, MaxTailLines)
	if err != nil || len(got.Lines) != 524 || got.Lines[0].Offset != 76*2000 || len(got.Lines[0].Text) != 1999 {
		t.Fatalf("tail(big) = %d lines, first %+v, %v; want 524 from %d", len(got.Lines), got.Lines[:min(len(got.Lines), 1)], err, 76*2000)
	}
	// A line over maxLineBytes is cut and flagged.
	put(t, home, "long", strings.Repeat("b", maxLineBytes+10)+"\nshort\n")
	got, err = tail(root, "long", -1, 2)
	if err != nil || len(got.Lines) != 2 || len(got.Lines[0].Text) != maxLineBytes || !got.Lines[0].Cut ||
		got.Lines[1] != (Line{Offset: int64(maxLineBytes + 11), Text: "short"}) {
		t.Errorf("tail(long) = %d lines, %v", len(got.Lines), err)
	}
	// A window inside one long line comes back as that piece of the line.
	put(t, home, "giant", strings.Repeat("c", MaxReadBytes+100))
	got, err = tail(root, "giant", -1, 1)
	if err != nil || len(got.Lines) != 1 || got.Lines[0].Offset != 100 || len(got.Lines[0].Text) != maxLineBytes || !got.Lines[0].Cut {
		t.Errorf("tail(giant) = %d lines, %v", len(got.Lines), err)
	}
}

func TestSearch(t *testing.T) {
	root, home, _ := testHome(t)
	// Offsets: 0, 10, 18, 31, 47; the file is 59 bytes.
	put(t, home, "log", "Error one\nnothing\nan ERROR two\nliteral .* here\nerror three\n")
	for _, c := range []struct {
		from    int64
		query   string
		max     int
		want    []int64
		next    int64
		stopped string
	}{
		{0, "error", 10, []int64{0, 18, 47}, 59, "end"},
		{0, ".*", 10, []int64{31}, 59, "end"}, // plain text, never a regex
		{10, "error", 1, []int64{18}, 31, "matches"},
		{31, "ERROR", 10, []int64{47}, 59, "end"},
		{59, "error", 10, []int64{}, 59, "end"},
	} {
		res, err := search(root, "log", c.from, contract.LogSearch(c.query), c.max, fixedClock)
		var got []int64
		for _, m := range res.Matches {
			got = append(got, m.Offset)
		}
		if err != nil || !slices.Equal(got, c.want) || res.Next != c.next || res.Stopped != c.stopped {
			t.Errorf("search(%d, %q, %d) = %v next %d %q, %v; want %v next %d %q",
				c.from, c.query, c.max, got, res.Next, res.Stopped, err, c.want, c.next, c.stopped)
		}
	}
	// Each read of the clock moves it 4 s: the search ends once 10 s have passed.
	now := fixedClock()
	tick := func() time.Time { now = now.Add(4 * time.Second); return now }
	res, err := search(root, "log", 0, "error", 10, tick)
	if err != nil || len(res.Matches) != 1 || res.Next != 18 || res.Stopped != "time" {
		t.Errorf("slow search = %+v, %v; want one match, next 18, stopped by time", res, err)
	}
	for _, c := range []struct {
		from  int64
		query string
		max   int
	}{{-1, "x", 10}, {0, "x", 0}, {0, "x", MaxSearchMatches + 1}, {0, "bad\x01", 10}} {
		if _, err := search(root, "log", c.from, contract.LogSearch(c.query), c.max, fixedClock); err == nil {
			t.Errorf("search(%d, %q, %d): no error", c.from, c.query, c.max)
		}
	}
}

func TestSearchStopsAtTextLimit(t *testing.T) {
	root, home, _ := testHome(t)
	line := strings.Repeat("x", maxLineBytes-6) + " match\n"
	put(t, home, "log", strings.Repeat(line, 70))
	res, err := search(root, "log", 0, "match", MaxSearchMatches, fixedClock)
	want := MaxReadBytes / maxLineBytes
	if err != nil || len(res.Matches) != want || res.Stopped != "size" || res.Next != int64(want*len(line)) {
		t.Errorf("search = %d matches, next %d, %q, %v; want %d, stopped by size", len(res.Matches), res.Next, res.Stopped, err, want)
	}
}
