package configtx

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"zimam/agent/internal/paths"
)

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// change writes a file owned by the test's own user, so Apply's fchown works
// without root.
func change(path, content string) Change {
	return Change{Path: path, Content: []byte(content), Mode: 0o644, UID: os.Getuid(), GID: os.Getgid()}
}

func pass(context.Context) error { return nil }

// failOnce fails the first test with output, and passes every later one.
func failOnce(output string) func(context.Context) error {
	calls := 0
	return func(context.Context) error {
		calls++
		if calls == 1 {
			return &TestFailed{Output: output}
		}
		return nil
	}
}

// noLeftovers fails the test if dir still holds .zimam-prev or .zimam-tmp
// files.
func noLeftovers(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".zimam-prev") || strings.HasSuffix(e.Name(), ".zimam-tmp") {
			t.Errorf("%s was left behind", e.Name())
		}
	}
}

func TestApplyKeepsNewFilesWhenTestPasses(t *testing.T) {
	dir := t.TempDir()
	existing := filepath.Join(dir, "existing.conf")
	added := filepath.Join(dir, "added.conf")
	removed := filepath.Join(dir, "removed.conf")
	writeFile(t, existing, "old\n")
	writeFile(t, removed, "going\n")

	tests := 0
	test := func(context.Context) error {
		tests++
		if got := readFile(t, existing); got != "new\n" {
			t.Errorf("the test saw %q in existing.conf, want the new content", got)
		}
		return nil
	}
	err := Apply(context.Background(), []Change{
		change(existing, "new\n"),
		change(added, "added\n"),
		{Path: removed, Remove: true},
	}, test)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if tests != 1 {
		t.Errorf("test ran %d times, want 1", tests)
	}
	if got := readFile(t, existing); got != "new\n" {
		t.Errorf("existing.conf = %q, want the new content", got)
	}
	if got := readFile(t, added); got != "added\n" {
		t.Errorf("added.conf = %q", got)
	}
	if _, err := os.Lstat(removed); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("removed.conf still exists (Lstat: %v)", err)
	}
	noLeftovers(t, dir)
}

func TestApplyRestoresAndRetestsOnFailure(t *testing.T) {
	dir := t.TempDir()
	conf := filepath.Join(dir, "site.conf")
	writeFile(t, conf, "old\n")

	var seen []string
	test := func(context.Context) error {
		seen = append(seen, readFile(t, conf))
		if len(seen) == 1 {
			return &TestFailed{Output: `nginx: [emerg] unexpected "}" in site.conf:3`}
		}
		return nil
	}
	err := Apply(context.Background(), []Change{change(conf, "new\n")}, test)
	var failed *TestFailed
	if !errors.As(err, &failed) {
		t.Fatalf("Apply = %v, want a *TestFailed", err)
	}
	if !strings.Contains(err.Error(), `unexpected "}" in site.conf:3`) {
		t.Errorf("the error doesn't carry the test output: %v", err)
	}
	// The new file was tested, then the restored one was tested again.
	if want := []string{"new\n", "old\n"}; !slices.Equal(seen, want) {
		t.Errorf("the tests saw %q, want %q", seen, want)
	}
	if got := readFile(t, conf); got != "old\n" {
		t.Errorf("site.conf = %q, want the old content back", got)
	}
	noLeftovers(t, dir)
}

func TestApplyRemovesNewFileOnFailure(t *testing.T) {
	dir := t.TempDir()
	conf := filepath.Join(dir, "new.conf")
	if err := Apply(context.Background(), []Change{change(conf, "new\n")}, failOnce("bad")); err == nil {
		t.Fatal("Apply succeeded although the test failed")
	}
	if _, err := os.Lstat(conf); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("new.conf is still there after the failed test (Lstat: %v)", err)
	}
	noLeftovers(t, dir)
}

func TestApplyRemoveChangeIsRestored(t *testing.T) {
	dir := t.TempDir()
	conf := filepath.Join(dir, "www.conf")
	writeFile(t, conf, "[www]\n")

	goneDuringTest := false
	tests := 0
	test := func(context.Context) error {
		tests++
		if tests == 1 {
			_, err := os.Lstat(conf)
			goneDuringTest = errors.Is(err, fs.ErrNotExist)
			return &TestFailed{Output: "bad"}
		}
		return nil
	}
	if err := Apply(context.Background(), []Change{{Path: conf, Remove: true}}, test); err == nil {
		t.Fatal("Apply succeeded although the test failed")
	}
	if !goneDuringTest {
		t.Error("www.conf was still in place while the test ran")
	}
	if got := readFile(t, conf); got != "[www]\n" {
		t.Errorf("www.conf = %q, want it restored", got)
	}
	noLeftovers(t, dir)
}

func TestApplyRefusesSymlinkTarget(t *testing.T) {
	dir := t.TempDir()
	first := filepath.Join(dir, "first.conf")
	writeFile(t, first, "old\n")
	target := filepath.Join(dir, "shadow")
	writeFile(t, target, "secret\n")
	link := filepath.Join(dir, "link.conf")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}

	err := Apply(context.Background(), []Change{change(first, "new\n"), change(link, "pwned\n")}, pass)
	if err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("Apply = %v, want a not-a-regular-file error", err)
	}
	if got := readFile(t, target); got != "secret\n" {
		t.Errorf("the symlink's target was written: %q", got)
	}
	if fi, err := os.Lstat(link); err != nil || fi.Mode()&fs.ModeSymlink == 0 {
		t.Errorf("link.conf is no longer a symlink (Lstat: %v)", err)
	}
	if got := readFile(t, first); got != "old\n" {
		t.Errorf("first.conf = %q, want the earlier change undone", got)
	}
	noLeftovers(t, dir)
}

func TestApplyRefusesDuplicatePaths(t *testing.T) {
	dir := t.TempDir()
	conf := filepath.Join(dir, "site.conf")
	writeFile(t, conf, "old\n")

	tests := 0
	count := func(context.Context) error { tests++; return nil }
	err := Apply(context.Background(), []Change{
		change(conf, "one\n"),
		change(filepath.Join(dir, ".", "site.conf"), "two\n"),
	}, count)
	if err == nil || !strings.Contains(err.Error(), "more than once") {
		t.Fatalf("Apply = %v, want a more-than-once error", err)
	}
	if got := readFile(t, conf); got != "old\n" {
		t.Errorf("site.conf = %q, want it untouched", got)
	}
	if tests != 0 {
		t.Errorf("test ran %d times for a refused set", tests)
	}
	noLeftovers(t, dir)
}

func TestApplySetsModeAndOwner(t *testing.T) {
	dir := t.TempDir()
	conf := filepath.Join(dir, "htpasswd")
	c := change(conf, "alice:hash\n")
	c.Mode = 0o640
	root := os.Geteuid() == 0
	if root {
		c.UID, c.GID = 1234, 5678
	}
	if err := Apply(context.Background(), []Change{c}, pass); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	fi, err := os.Stat(conf)
	if err != nil {
		t.Fatal(err)
	}
	if got := fi.Mode().Perm(); got != 0o640 {
		t.Errorf("mode = %v, want 0640", got)
	}
	if !root {
		t.Log("not running as root, so the owner isn't checked")
		return
	}
	st := fi.Sys().(*syscall.Stat_t)
	if st.Uid != 1234 || st.Gid != 5678 {
		t.Errorf("owner = %d:%d, want 1234:5678", st.Uid, st.Gid)
	}
}

func TestReloadLockSerialises(t *testing.T) {
	p := paths.Paths{Root: t.TempDir()}
	if err := os.MkdirAll(p.LocksDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	var active, most atomic.Int32
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			err := WithReloadLock(context.Background(), p, func() error {
				n := active.Add(1)
				for m := most.Load(); n > m && !most.CompareAndSwap(m, n); m = most.Load() {
				}
				time.Sleep(20 * time.Millisecond)
				active.Add(-1)
				return nil
			})
			if err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if got := most.Load(); got != 1 {
		t.Errorf("%d holders at once, want 1", got)
	}
	if _, err := os.Stat(p.ReloadLock()); err != nil {
		t.Errorf("the lock file: %v", err)
	}

	boom := errors.New("boom")
	if err := WithReloadLock(context.Background(), p, func() error { return boom }); !errors.Is(err, boom) {
		t.Errorf("WithReloadLock = %v, want fn's error", err)
	}
}
