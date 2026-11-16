package maintenance

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"zimam/agent/contract"
	"zimam/agent/internal/helper"
	"zimam/agent/internal/paths"
	"zimam/agent/internal/sites"
	"zimam/agent/internal/sysexec"
)

// homeHelper is a site's logs/ folder in memory, as the helper lists it,
// and a laravel site's site/shared/storage/logs when shared is set.
type homeHelper struct {
	entries map[string]helper.FileInfo // by name inside logs/
	shared  map[string]helper.FileInfo // by name inside site/shared/storage/logs; nil: not there
	removed []string
	listed  int
	dirs    []string // the directories listed, in order
}

const laravelLogs = "site/shared/storage/logs"

func (h *homeHelper) List(_ context.Context, a helper.Account, dir string) (helper.Listing, error) {
	h.listed++
	h.dirs = append(h.dirs, dir)
	entries := h.entries
	switch {
	case dir == laravelLogs && h.shared == nil:
		return helper.Listing{}, fmt.Errorf("list %s: %w", dir, fs.ErrNotExist)
	case dir == laravelLogs:
		entries = h.shared
	case dir != "logs":
		return helper.Listing{}, fmt.Errorf("listed %s", dir)
	}
	var l helper.Listing
	for _, fi := range entries {
		l.Entries = append(l.Entries, fi)
	}
	sort.Slice(l.Entries, func(i, j int) bool { return l.Entries[i].Name < l.Entries[j].Name })
	return l, nil
}

func (h *homeHelper) RemoveTree(_ context.Context, a helper.Account, name string) error {
	h.removed = append(h.removed, a.Name+" "+name)
	if rest, ok := strings.CutPrefix(name, laravelLogs+"/"); ok {
		delete(h.shared, rest)
		return nil
	}
	delete(h.entries, strings.TrimPrefix(name, "logs/"))
	return nil
}

var capNow = time.Date(2026, 11, 3, 9, 0, 0, 0, time.UTC)

type capEnv struct {
	t     *testing.T
	d     sysexec.Deps
	h     *homeHelper
	homes Homes
	dir   string // /var/log/zimam/sites/shop
}

func newCapEnv(t *testing.T, capBytes int64) *capEnv {
	t.Helper()
	p := paths.Paths{Root: t.TempDir()}
	if err := sites.Save(p, sites.State{Name: "shop", Domain: "shop.example.com", Type: contract.SiteStatic, TemplateVersion: 1, Security: sites.DefaultSecurity()}); err != nil {
		t.Fatal(err)
	}
	cfg := testConfig(t, p)
	cfg.SiteLogCapBytes = contract.SiteCapBytes(capBytes)
	h := &homeHelper{entries: map[string]helper.FileInfo{}}
	e := &capEnv{t: t, h: h, dir: p.SiteLogDir("shop"),
		d: sysexec.Deps{Paths: p, Run: sysexec.NewFakeRunner(), Config: cfg, Logf: t.Logf, Now: func() time.Time { return capNow }},
		homes: Homes{Helper: h, Lookup: func(name string) (helper.Account, error) {
			return helper.Account{Name: name, UID: 1000, GID: 1000, Home: "/home/" + name}, nil
		}},
	}
	if err := os.MkdirAll(e.dir, 0o750); err != nil {
		t.Fatal(err)
	}
	return e
}

// root writes a file of size bytes in the site's Nginx log folder, last
// changed age ago.
func (e *capEnv) root(name string, size int, age time.Duration) {
	e.t.Helper()
	path := filepath.Join(e.dir, name)
	if err := os.WriteFile(path, make([]byte, size), 0o640); err != nil {
		e.t.Fatal(err)
	}
	if err := os.Chtimes(path, capNow.Add(-age), capNow.Add(-age)); err != nil {
		e.t.Fatal(err)
	}
}

func (e *capEnv) home(name string, size int64, age time.Duration, mode fs.FileMode) {
	e.h.entries[name] = helper.FileInfo{Name: name, Size: size, BlocksBytes: size, Mode: mode, ModTime: capNow.Add(-age)}
}

func (e *capEnv) rootNames() []string {
	entries, _ := os.ReadDir(e.dir)
	var out []string
	for _, de := range entries {
		out = append(out, de.Name())
	}
	return out
}

// blk is one allocation unit: root's test files take whole 4096-byte blocks
// on disk, and the cap counts allocated bytes.
const blk = 4096

// Over the cap, home archives go first, oldest first, while root's files
// plus the home's are over it; root's archives go only when root's files
// alone are.
func TestCapDeletesOldestFirst(t *testing.T) {
	e := newCapEnv(t, 10*blk)
	e.root("access.log", 3*blk, 0)
	e.root("access.log-20261101-1761955200.gz", 2*blk, 50*time.Hour)
	e.root("access.log-20261102-1762041600.gz", 2*blk, 26*time.Hour)
	e.home("php-error.log", blk, 0, 0o640)
	e.home("php-error.log-20261031-1761868800.gz", 3*blk, 70*time.Hour, 0o640)
	e.home("php-error.log-20261102-1762041600.gz", blk, 20*time.Hour, 0o640)

	deleted, err := enforceCaps(context.Background(), e.d, e.homes)
	if err != nil {
		t.Fatal(err)
	}
	// 12 units: the 70 h home archive goes (9), and that fits.
	if want := []string{"/home/s_shop/logs/php-error.log-20261031-1761868800.gz"}; !slices.Equal(deleted, want) {
		t.Errorf("deleted %v, want %v", deleted, want)
	}
	if want := []string{"s_shop logs/php-error.log-20261031-1761868800.gz"}; !slices.Equal(e.h.removed, want) {
		t.Errorf("helper removed %v", e.h.removed)
	}
	if got := e.rootNames(); len(got) != 3 {
		t.Errorf("root files %v", got)
	}

	e.d.Config.SiteLogCapBytes = 5 * blk
	deleted, _ = enforceCaps(context.Background(), e.d, e.homes)
	// 9 units: the remaining home archive goes (8), root is still at 7, so
	// its oldest archive goes too (5), and that fits.
	want := []string{"/home/s_shop/logs/php-error.log-20261102-1762041600.gz", e.dir + "/access.log-20261101-1761955200.gz"}
	if !slices.Equal(deleted, want) {
		t.Errorf("deleted %v, want %v", deleted, want)
	}
	// Current logs are never deleted, even while over the cap.
	if got := e.rootNames(); !slices.Equal(got, []string{"access.log", "access.log-20261102-1762041600.gz"}) {
		t.Errorf("root files %v", got)
	}
}

// Sparse, future-dated archives in the site's own logs/ use no disk, so they
// never push root's Nginx archives out (audit variant A).
func TestSparseHomeArchivesNeverDeleteRootArchives(t *testing.T) {
	e := newCapEnv(t, 1<<30)
	e.root("access.log-20261101-1761955200.gz", blk, 50*time.Hour)
	e.root("error.log-20261102-1762041600.gz", blk, 26*time.Hour)
	future := time.Date(2099, 12, 31, 0, 0, 0, 0, time.UTC)
	for i := range 6 {
		name := fmt.Sprintf("y%d.log-20991231-%d.gz", i, i)
		e.h.entries[name] = helper.FileInfo{Name: name, Size: 200 << 20, BlocksBytes: 0, Mode: 0o640, ModTime: future}
	}
	deleted, err := enforceCaps(context.Background(), e.d, e.homes)
	if err != nil || len(deleted) != 0 || len(e.h.removed) != 0 {
		t.Errorf("deleted %v, removed %v, err %v", deleted, e.h.removed, err)
	}
	if got := e.rootNames(); len(got) != 2 {
		t.Errorf("root files %v", got)
	}
}

// Home archives with real bytes push the total over the cap: they go oldest
// first, and root's archives stay while root's files alone fit.
func TestRealHomeArchivesDeleteHomeFirst(t *testing.T) {
	e := newCapEnv(t, 5*blk)
	e.root("access.log", blk, 0)
	e.root("access.log-20261101-1761955200.gz", blk, 50*time.Hour)
	e.home("a.log-20261030-1.gz", 2*blk, 30*time.Hour, 0o640)
	e.home("b.log-20261031-2.gz", 2*blk, 20*time.Hour, 0o640)
	e.home("c.log-20261101-3.gz", 2*blk, 10*time.Hour, 0o640)
	deleted, err := enforceCaps(context.Background(), e.d, e.homes)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"/home/s_shop/logs/a.log-20261030-1.gz", "/home/s_shop/logs/b.log-20261031-2.gz"}
	if !slices.Equal(deleted, want) {
		t.Errorf("deleted %v, want %v", deleted, want)
	}
	if got := e.rootNames(); len(got) != 2 {
		t.Errorf("root files %v", got)
	}
}

// Root's own files over the cap: its archives go, oldest first.
func TestRootOverCapAloneDeletesRootArchives(t *testing.T) {
	e := newCapEnv(t, 2*blk)
	e.root("access.log", blk, 0)
	e.root("access.log-20261101-1761955200.gz", blk, 50*time.Hour)
	e.root("access.log-20261102-1762041600.gz", blk, 26*time.Hour)
	deleted, err := enforceCaps(context.Background(), e.d, e.homes)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{e.dir + "/access.log-20261101-1761955200.gz"}; !slices.Equal(deleted, want) {
		t.Errorf("deleted %v, want %v", deleted, want)
	}
}

// A home archive dated in the future sorts at now, not at its date: two
// future files tie, and the path decides, where their dates would not.
func TestFutureMtimeIsClampedToNow(t *testing.T) {
	e := newCapEnv(t, 2*blk)
	e.root("access.log", blk, 0)
	e.home("old.log-20261101-1.gz", blk, 10*time.Hour, 0o640)
	e.home("a.log-20991231-2.gz", blk, -(24 * 365 * 70 * time.Hour), 0o640) // 2099-ish
	e.home("b.log-20981231-3.gz", blk, -(24 * 365 * 60 * time.Hour), 0o640)
	deleted, err := enforceCaps(context.Background(), e.d, e.homes)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"/home/s_shop/logs/old.log-20261101-1.gz", "/home/s_shop/logs/a.log-20991231-2.gz"}
	if !slices.Equal(deleted, want) {
		t.Errorf("deleted %v, want %v", deleted, want)
	}
}

// failingList is a helper whose listing fails for one site user.
type failingList struct {
	*homeHelper
	user string
}

func (f failingList) List(ctx context.Context, a helper.Account, dir string) (helper.Listing, error) {
	if a.Name == f.user {
		return helper.Listing{}, fmt.Errorf("list %s: helper failed", dir)
	}
	return f.homeHelper.List(ctx, a, dir)
}

// The helper failing for one site skips that site only: its root archives
// stay, and the next site's cap still runs.
func TestCapHelperFailureSkipsOnlyThatSite(t *testing.T) {
	e := newCapEnv(t, blk)
	for _, n := range []contract.SiteName{"alpha", "beta"} {
		if err := sites.Save(e.d.Paths, sites.State{Name: n, Domain: contract.Domain(string(n) + ".example.com"), Type: contract.SiteStatic, TemplateVersion: 1, Security: sites.DefaultSecurity()}); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(e.d.Paths.SiteLogDir(n), 0o750); err != nil {
			t.Fatal(err)
		}
		for _, f := range []string{"access.log-20261101-1761955200.gz", "access.log-20261102-1762041600.gz"} {
			if err := os.WriteFile(filepath.Join(e.d.Paths.SiteLogDir(n), f), make([]byte, blk), 0o640); err != nil {
				t.Fatal(err)
			}
		}
	}
	homes := e.homes
	homes.Helper = failingList{e.h, "s_alpha"}
	deleted, err := enforceCaps(context.Background(), e.d, homes)
	if err == nil || !strings.Contains(err.Error(), "site alpha's logs") {
		t.Errorf("err = %v, want site a's listing failure", err)
	}
	for _, d := range deleted {
		if strings.Contains(d, "/alpha/") {
			t.Errorf("deleted %s of the site whose listing failed", d)
		}
	}
	if len(deleted) != 1 || !strings.Contains(deleted[0], "/beta/access.log-20261101") {
		t.Errorf("deleted %v, want site beta's oldest archive", deleted)
	}
}

// Inside the home everything goes through the helper: root lists nothing
// and deletes nothing there itself.
func TestCapUsesHelperInsideHome(t *testing.T) {
	e := newCapEnv(t, 100)
	e.home("php-error.log-20261031-1761868800.gz", 500, time.Hour, 0o640)
	home := e.d.Paths.SiteHomeLogsDir("shop")
	if err := os.MkdirAll(home, 0o755); err != nil {
		t.Fatal(err)
	}
	// A file root could see if it looked: it must not be counted or touched.
	os.WriteFile(filepath.Join(home, "php-error.log-20261030-1.gz"), make([]byte, 900), 0o640)
	if _, err := enforceCaps(context.Background(), e.d, e.homes); err != nil {
		t.Fatal(err)
	}
	if e.h.listed != 1 || len(e.h.removed) != 1 {
		t.Errorf("listed %d times, removed %v", e.h.listed, e.h.removed)
	}
	if _, err := os.Stat(filepath.Join(home, "php-error.log-20261030-1.gz")); err != nil {
		t.Errorf("root touched the home: %v", err)
	}
}

// A link in logs/ to /etc/passwd, or in the Nginx folder, is neither
// counted nor deleted.
func TestCapIgnoresSymlinks(t *testing.T) {
	e := newCapEnv(t, 100)
	e.home("php-error.log-20261031-1761868800.gz", 1<<40, time.Hour, fs.ModeSymlink|0o777)
	e.home("passwd.log", 1<<40, time.Hour, fs.ModeSymlink|0o777)
	if err := os.Symlink("/etc/passwd", filepath.Join(e.dir, "access.log-20261101-1761955200.gz")); err != nil {
		t.Fatal(err)
	}
	deleted, err := enforceCaps(context.Background(), e.d, e.homes)
	if err != nil || len(deleted) != 0 || len(e.h.removed) != 0 {
		t.Errorf("deleted %v, removed %v, err %v", deleted, e.h.removed, err)
	}
	if _, err := os.Lstat(filepath.Join(e.dir, "access.log-20261101-1761955200.gz")); err != nil {
		t.Errorf("the link was removed: %v", err)
	}
}

// Only regular files named like an archive are deleted; other files count
// but stay.
func TestCapOnlyDeletesArchives(t *testing.T) {
	e := newCapEnv(t, 100)
	e.root("access.log", 400, 100*time.Hour)
	e.root("notes.txt", 400, 200*time.Hour)
	e.root("access.log.1", 400, 300*time.Hour)
	e.home("debug.log", 400, 100*time.Hour, 0o640)
	deleted, err := enforceCaps(context.Background(), e.d, e.homes)
	if err != nil || len(deleted) != 0 || len(e.h.removed) != 0 {
		t.Errorf("deleted %v, removed %v, err %v", deleted, e.h.removed, err)
	}
}

// Archives older than log_retention_days go even under the cap, wherever
// they are: logrotate's maxage only deletes when the log itself rotates,
// so an idle site's archives would never age out (§11.3, done-when 10).
// Current logs stay, however old, and so do links.
func TestRetentionDeletesOldArchives(t *testing.T) {
	e := newCapEnv(t, 1<<40)
	e.d.Config.LogRetentionDays = 1
	e.root("access.log", 10, 100*time.Hour)
	e.root("access.log-20261031-1761868800.gz", 10, 72*time.Hour)
	e.root("access.log-20261102-1762041600.gz", 10, 23*time.Hour)
	e.home("php-error.log", 10, 100*time.Hour, 0o640)
	e.home("php-error.log-20261031-1761868800.gz", 10, 25*time.Hour, 0o640)
	e.home("php-error.log-20261029-1761696000.gz", 10, 120*time.Hour, fs.ModeSymlink|0o777)

	deleted, err := enforceCaps(context.Background(), e.d, e.homes)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{e.dir + "/access.log-20261031-1761868800.gz", "/home/s_shop/logs/php-error.log-20261031-1761868800.gz"}
	if !slices.Equal(deleted, want) {
		t.Errorf("deleted %v, want %v", deleted, want)
	}
	if got := e.rootNames(); !slices.Equal(got, []string{"access.log", "access.log-20261102-1762041600.gz"}) {
		t.Errorf("root files %v", got)
	}
	if want := []string{"s_shop logs/php-error.log-20261031-1761868800.gz"}; !slices.Equal(e.h.removed, want) {
		t.Errorf("helper removed %v", e.h.removed)
	}
}

// laravel makes shop a laravel site, with its shared storage logs.
func (e *capEnv) laravel() {
	e.t.Helper()
	if err := sites.Save(e.d.Paths, sites.State{Name: "shop", Domain: "shop.example.com", Type: contract.SiteLaravel, PHPVersion: "8.4", TemplateVersion: 2, Security: sites.DefaultSecurity()}); err != nil {
		e.t.Fatal(err)
	}
	e.h.shared = map[string]helper.FileInfo{}
}

func (e *capEnv) shared(name string, size int64, age time.Duration, mode fs.FileMode) {
	e.h.shared[name] = helper.FileInfo{Name: name, Size: size, BlocksBytes: size, Mode: mode, ModTime: capNow.Add(-age)}
}

// A laravel site's Laravel logs count as the home's (R02-D8): its archives
// go oldest first, with logs/'s, through the helper as the site user, while
// the site is over the cap. The live laravel.log is never deleted, and
// root's Nginx archives stay while root's files alone fit.
func TestCapCountsLaravelLogs(t *testing.T) {
	e := newCapEnv(t, 6*blk)
	e.laravel()
	e.root("access.log", blk, 0)
	e.root("access.log-20261101-1761955200.gz", blk, 80*time.Hour)
	e.home("php-error.log-20261101-1761955200.gz", blk, 40*time.Hour, 0o640)
	e.shared("laravel.log", 3*blk, 0, 0o640)
	e.shared("laravel.log-20261031-1761868800.gz", 2*blk, 60*time.Hour, 0o640)
	e.shared("laravel.log-20261102-1762041600.gz", 2*blk, 20*time.Hour, 0o640)
	e.shared("evil.log-20261030-1.gz", 1<<40, 90*time.Hour, fs.ModeSymlink|0o777)

	deleted, err := enforceCaps(context.Background(), e.d, e.homes)
	if err != nil {
		t.Fatal(err)
	}
	// 10 units: the 60 h laravel archive goes (8), then the 40 h php-error
	// one (7), then the 20 h laravel one (5), and that fits. Root's 80 h
	// archive stays: root's files alone are 2.
	want := []string{
		"/home/s_shop/" + laravelLogs + "/laravel.log-20261031-1761868800.gz",
		"/home/s_shop/logs/php-error.log-20261101-1761955200.gz",
		"/home/s_shop/" + laravelLogs + "/laravel.log-20261102-1762041600.gz",
	}
	if !slices.Equal(deleted, want) {
		t.Errorf("deleted %v, want %v", deleted, want)
	}
	wantRemoved := []string{
		"s_shop " + laravelLogs + "/laravel.log-20261031-1761868800.gz",
		"s_shop logs/php-error.log-20261101-1761955200.gz",
		"s_shop " + laravelLogs + "/laravel.log-20261102-1762041600.gz",
	}
	if !slices.Equal(e.h.removed, wantRemoved) {
		t.Errorf("helper removed %v, want %v", e.h.removed, wantRemoved)
	}
	if _, ok := e.h.shared["laravel.log"]; !ok {
		t.Error("the live laravel.log was deleted")
	}
	if _, ok := e.h.shared["evil.log-20261030-1.gz"]; !ok {
		t.Error("a symlink in the Laravel logs was deleted")
	}
	if got := e.rootNames(); len(got) != 2 {
		t.Errorf("root files %v", got)
	}

	// Still over the cap with only the live log left: nothing more goes.
	e.d.Config.SiteLogCapBytes = blk
	if deleted, err := enforceCaps(context.Background(), e.d, e.homes); err != nil || slices.ContainsFunc(deleted, func(d string) bool { return strings.Contains(d, laravelLogs) }) {
		t.Errorf("deleted %v, %v", deleted, err)
	}
	if _, ok := e.h.shared["laravel.log"]; !ok {
		t.Error("the live laravel.log was deleted over the cap")
	}
}

// Old Laravel archives age out with the retention like the home's others.
func TestRetentionDeletesOldLaravelArchives(t *testing.T) {
	e := newCapEnv(t, 1<<30)
	e.laravel()
	e.shared("laravel.log-20261001-1.gz", blk, time.Duration(e.d.Config.LogRetentionDays+1)*24*time.Hour, 0o640)
	e.shared("laravel.log-20261102-2.gz", blk, time.Hour, 0o640)
	deleted, err := enforceCaps(context.Background(), e.d, e.homes)
	if err != nil || !slices.Equal(deleted, []string{"/home/s_shop/" + laravelLogs + "/laravel.log-20261001-1.gz"}) {
		t.Errorf("deleted %v, %v", deleted, err)
	}
}

// A laravel site without the shared logs folder yet is fine; a static or
// PHP site's shared storage is never listed, whatever is there.
func TestCapLaravelLogsOnlyForLaravelSites(t *testing.T) {
	e := newCapEnv(t, blk)
	e.laravel()
	e.h.shared = nil
	if _, err := enforceCaps(context.Background(), e.d, e.homes); err != nil {
		t.Errorf("a laravel site without shared logs: %v", err)
	}

	e = newCapEnv(t, blk)
	e.h.shared = map[string]helper.FileInfo{}
	e.shared("laravel.log-20261031-1761868800.gz", 4*blk, 60*time.Hour, 0o640)
	deleted, err := enforceCaps(context.Background(), e.d, e.homes)
	if err != nil || len(deleted) != 0 {
		t.Errorf("a static site: deleted %v, %v", deleted, err)
	}
	if !slices.Equal(e.h.dirs, []string{"logs"}) {
		t.Errorf("a static site listed %v, want only logs", e.h.dirs)
	}
}

// failingShared is a helper whose listing of a laravel site's shared logs
// fails, while logs/ lists.
type failingShared struct{ *homeHelper }

func (f failingShared) List(ctx context.Context, a helper.Account, dir string) (helper.Listing, error) {
	if dir == laravelLogs {
		return helper.Listing{}, fmt.Errorf("list %s: helper failed", dir)
	}
	return f.homeHelper.List(ctx, a, dir)
}

// The shared logs' listing failing is reported, but the site's other
// budgets still run: root's Nginx archives and the home's logs/ archives
// are still capped from what was listed.
func TestCapLaravelListingFailureKeepsOtherBudgets(t *testing.T) {
	e := newCapEnv(t, 2*blk)
	e.laravel()
	e.root("access.log", blk, 0)
	e.root("access.log-20261101-1761955200.gz", 2*blk, 50*time.Hour)
	e.root("access.log-20261102-1762041600.gz", blk, 26*time.Hour)
	e.home("php-error.log-20261101-1761955200.gz", blk, 40*time.Hour, 0o640)
	homes := e.homes
	homes.Helper = failingShared{e.h}
	deleted, err := enforceCaps(context.Background(), e.d, homes)
	if err == nil || !strings.Contains(err.Error(), "site shop's logs") || !strings.Contains(err.Error(), laravelLogs) {
		t.Errorf("err = %v, want the shared listing's failure", err)
	}
	// 5 units: the home archive goes (4), then root's oldest (2), and that fits.
	want := []string{"/home/s_shop/logs/php-error.log-20261101-1761955200.gz", e.dir + "/access.log-20261101-1761955200.gz"}
	if !slices.Equal(deleted, want) {
		t.Errorf("deleted %v, want %v", deleted, want)
	}
}
