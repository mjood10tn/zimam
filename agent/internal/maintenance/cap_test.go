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

// homeHelper is a site's logs/ folder in memory, as the helper lists it.
type homeHelper struct {
	entries map[string]helper.FileInfo // by name inside logs/
	removed []string
	listed  int
}

func (h *homeHelper) List(_ context.Context, a helper.Account, dir string) (helper.Listing, error) {
	h.listed++
	if dir != "logs" {
		return helper.Listing{}, fmt.Errorf("listed %s", dir)
	}
	var l helper.Listing
	for _, fi := range h.entries {
		l.Entries = append(l.Entries, fi)
	}
	sort.Slice(l.Entries, func(i, j int) bool { return l.Entries[i].Name < l.Entries[j].Name })
	return l, nil
}

func (h *homeHelper) RemoveTree(_ context.Context, a helper.Account, name string) error {
	h.removed = append(h.removed, a.Name+" "+name)
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
	e.h.entries[name] = helper.FileInfo{Name: name, Size: size, Mode: mode, ModTime: capNow.Add(-age)}
}

func (e *capEnv) rootNames() []string {
	entries, _ := os.ReadDir(e.dir)
	var out []string
	for _, de := range entries {
		out = append(out, de.Name())
	}
	return out
}

// Over the cap, the oldest archives go first, wherever they are, until the
// total fits.
func TestCapDeletesOldestFirst(t *testing.T) {
	e := newCapEnv(t, 1000)
	e.root("access.log", 300, 0)
	e.root("access.log-20261101-1761955200.gz", 200, 50*time.Hour)
	e.root("access.log-20261102-1762041600.gz", 200, 26*time.Hour)
	e.home("php-error.log", 100, 0, 0o640)
	e.home("php-error.log-20261031-1761868800.gz", 250, 70*time.Hour, 0o640)
	e.home("php-error.log-20261102-1762041600.gz", 100, 20*time.Hour, 0o640)

	deleted, err := enforceCaps(context.Background(), e.d, e.homes)
	if err != nil {
		t.Fatal(err)
	}
	// 1150 bytes: the 70 h home archive goes (900), and that fits.
	if want := []string{"/home/s_shop/logs/php-error.log-20261031-1761868800.gz"}; !slices.Equal(deleted, want) {
		t.Errorf("deleted %v, want %v", deleted, want)
	}
	if want := []string{"s_shop logs/php-error.log-20261031-1761868800.gz"}; !slices.Equal(e.h.removed, want) {
		t.Errorf("helper removed %v", e.h.removed)
	}
	if got := e.rootNames(); len(got) != 3 {
		t.Errorf("root files %v", got)
	}

	e.d.Config.SiteLogCapBytes = 500
	deleted, _ = enforceCaps(context.Background(), e.d, e.homes)
	// 900 bytes: the two Nginx archives go, oldest first, and 500 fits.
	want := []string{e.dir + "/access.log-20261101-1761955200.gz", e.dir + "/access.log-20261102-1762041600.gz"}
	if !slices.Equal(deleted, want) {
		t.Errorf("deleted %v, want %v", deleted, want)
	}
	// Current logs are never deleted, even while over the cap.
	if got := e.rootNames(); !slices.Equal(got, []string{"access.log"}) {
		t.Errorf("root files %v", got)
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
