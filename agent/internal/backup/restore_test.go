package backup

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"zimam/agent/contract"
	"zimam/agent/internal/helper"
	"zimam/agent/internal/services"
	"zimam/agent/internal/sysexec"
)

const restoreID = contract.BackupID("20261122T033000Z")

func gz(t *testing.T, b []byte) []byte {
	t.Helper()
	var out bytes.Buffer
	w := gzip.NewWriter(&out)
	w.Write(b)
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

// siteTar is a files part with links of every kind: two inside the archive,
// one absolute (Laravel's storage:link makes those), one that climbs out,
// and one through another link.
func siteTar(t *testing.T) []byte {
	t.Helper()
	var b bytes.Buffer
	tw := tar.NewWriter(&b)
	add := func(h *tar.Header, body string) {
		h.Size = int64(len(body))
		if h.Mode == 0 {
			h.Mode = 0o640
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		io.WriteString(tw, body)
	}
	add(&tar.Header{Name: "site/shared/", Typeflag: tar.TypeDir, Mode: 0o750}, "")
	add(&tar.Header{Name: "site/shared/.env", Typeflag: tar.TypeReg}, "APP_KEY=x\n")
	add(&tar.Header{Name: "site/releases/initial/", Typeflag: tar.TypeDir, Mode: 0o750}, "")
	add(&tar.Header{Name: "site/releases/initial/index.html", Typeflag: tar.TypeReg}, "<h1>shop</h1>\n")
	for name, target := range map[string]string{
		".env": "../../shared/.env", "storage": "../../shared/storage",
		"public-storage": "/home/s_shop/site/shared/storage/app/public", "up": "../../../../etc", "through": "storage/app",
	} {
		add(&tar.Header{Name: "site/releases/initial/" + name, Typeflag: tar.TypeSymlink, Linkname: target, Mode: 0o777}, "")
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return gz(t, b.Bytes())
}

func partInfo(part contract.BackupPart, data []byte) contract.BackupPartInfo {
	sum := sha256.Sum256(data)
	return contract.BackupPartInfo{Part: part, Bytes: contract.DatabaseSize(len(data)), SHA256: hex.EncodeToString(sum[:])}
}

func manifestJSON(t *testing.T, id contract.BackupID, parts ...contract.BackupPartInfo) []byte {
	t.Helper()
	at, _ := time.Parse(idLayout, string(id))
	b, err := json.MarshalIndent(Manifest{Plan: "nightly", BackupInfo: contract.BackupInfo{Backup: id, StartedAt: at, FinishedAt: at.Add(time.Minute), Parts: parts},
		AgentVersion: "0.5.0", RcloneVersion: "1.76.0"}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return append(b, '\n')
}

// fakeCat answers rclone cat from objects, and records each call.
type fakeCat struct {
	objects map[string][]byte
	calls   [][]string
}

func (f *fakeCat) run(_ context.Context, _ sysexec.Deps, w io.Writer, args ...string) error {
	f.calls = append(f.calls, slices.Clone(args))
	b, ok := f.objects[strings.Join(args, " ")]
	if !ok {
		return &sysexec.ExitError{Path: "rclone", Code: 3, Stderr: []byte("directory not found")}
	}
	_, err := w.Write(b)
	return err
}

type restoreFixture struct {
	e       env
	re      RestoreEnv
	h       *fakeHelper
	cat     *fakeCat
	created []contract.DatabaseName
	dropped []contract.DatabaseName
	checked []contract.DatabaseName
}

// newRestore has planEnv's sites (shop with s_shop_main, blog with
// s_blog_main), MySQL installed, the nightly plan, and backup restoreID of
// it with shop's database and files.
func newRestore(t *testing.T, scripts ...sysexec.Script) *restoreFixture {
	t.Helper()
	e := planEnv(t, scripts...)
	addSite(t, e, "blog", "s_blog_main")
	if err := savePlans(e.d.Paths, []contract.BackupPlanParams{*nightlyPlan()}); err != nil {
		t.Fatal(err)
	}
	if err := services.Update(e.d.Paths, func(s *services.State) { s.MySQL = services.Entry{Installed: true, Version: "8.4.13"} }); err != nil {
		t.Fatal(err)
	}
	dump, files := gz(t, []byte(goodDump)), siteTar(t)
	dir := "offsite:nightly/" + string(restoreID) + "/"
	f := &restoreFixture{e: e, h: &fakeHelper{}, cat: &fakeCat{objects: map[string][]byte{
		"cat " + dir + "manifest.json":         manifestJSON(t, restoreID, partInfo("db-s_shop_main", dump), partInfo("files-shop", files)),
		"cat " + dir + "db-s_shop_main.sql.gz": dump,
		"cat " + dir + "files-shop.tar.gz":     files,
	}}}
	old := rcloneCat
	rcloneCat = f.cat.run
	t.Cleanup(func() { rcloneCat = old })
	f.re = RestoreEnv{
		Helper: f.h,
		Lookup: func(name string) (helper.Account, error) {
			return helper.Account{Name: name, UID: 1001, GID: 1001, Home: "/home/" + name}, nil
		},
		CheckNew: func(db contract.DatabaseName) error { f.checked = append(f.checked, db); return nil },
		Create:   func(_ context.Context, db contract.DatabaseName) error { f.created = append(f.created, db); return nil },
		Drop:     func(_ context.Context, db contract.DatabaseName) error { f.dropped = append(f.dropped, db); return nil },
	}
	return f
}

func mysqlRestore(db string, exit int) sysexec.Script {
	s := sysexec.Script{Path: "/usr/bin/mysql", Args: []string{"--protocol=socket", "--user=root", db}}
	if exit != 0 {
		s.Result = sysexec.Result{ExitCode: exit, Stderr: []byte("ERROR 1064 (42000) at line 2: You have an error in your SQL syntax")}
	}
	return s
}

func restoreParams(part contract.BackupPart, db contract.DatabaseName, overwrite bool) *contract.BackupRestoreParams {
	return &contract.BackupRestoreParams{Plan: "nightly", Backup: restoreID, Part: part, Site: "shop", Database: db, Overwrite: overwrite}
}

func mysqlCalls(e env) []sysexec.Call {
	return slices.DeleteFunc(e.fake.Calls(), func(c sysexec.Call) bool { return c.Path != "/usr/bin/mysql" })
}

func spoolEmpty(t *testing.T, e env) {
	t.Helper()
	entries, _ := os.ReadDir(e.d.Paths.RestoreSpoolDir())
	if len(entries) != 0 {
		t.Errorf("the spool holds %d files after the restore", len(entries))
	}
}

func TestListOffersOnlyCompleteBackups(t *testing.T) {
	f := newRestore(t)
	older := contract.BackupID("20261121T033000Z")
	f.cat.objects["cat --include /*/manifest.json offsite:nightly"] = append(manifestJSON(t, restoreID, partInfo("db-s_shop_main", []byte("x"))),
		manifestJSON(t, older, partInfo("db-s_shop_main", []byte("y")))...)
	res, err := List(context.Background(), f.e.d, "nightly")
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Backups) != 2 || res.Backups[0].Backup != older || res.Backups[1].Backup != restoreID || len(res.Backups[1].Parts) != 1 {
		t.Errorf("backups %+v, want both, oldest first", res.Backups)
	}
	// Only manifests are read: a backup without one never shows.
	if !slices.Equal(f.cat.calls[0], []string{"cat", "--include", "/*/manifest.json", "offsite:nightly"}) {
		t.Errorf("rclone %q", f.cat.calls[0])
	}
	delete(f.cat.objects, "cat --include /*/manifest.json offsite:nightly")
	if res, err := List(context.Background(), f.e.d, "nightly"); err != nil || res.Backups == nil || len(res.Backups) != 0 {
		t.Errorf("a plan without a folder: %+v, %v; want an empty list", res, err)
	}
	var ae *contract.AgentError
	if _, err := List(context.Background(), f.e.d, "gone"); !errors.As(err, &ae) || ae.Code != contract.ErrNotFound {
		t.Errorf("a missing plan: %v", err)
	}
}

func TestRestoreDatabaseIntoNewDatabase(t *testing.T) {
	f := newRestore(t, mysqlRestore("s_shop_copy", 0))
	res, err := Restore(context.Background(), f.e.d, f.re, restoreParams("db-s_shop_main", "s_shop_copy", false))
	if err != nil {
		t.Fatal(err)
	}
	if res.Database != "s_shop_copy" || res.Part != "db-s_shop_main" || res.Site != "shop" {
		t.Errorf("result %+v", res)
	}
	if !slices.Equal(f.checked, []contract.DatabaseName{"s_shop_copy"}) || !slices.Equal(f.created, f.checked) || len(f.dropped) != 0 {
		t.Errorf("checked %v, created %v, dropped %v", f.checked, f.created, f.dropped)
	}
	if calls := mysqlCalls(f.e); len(calls) != 1 || string(calls[0].Stdin) != goodDump {
		t.Errorf("mysql got %d runs, stdin %q", len(calls), calls)
	}
	spoolEmpty(t, f.e)
}

// A database that exists is overwritten only with the flag, and only one
// the site lists.
func TestRestoreOverwriteNeedsFlag(t *testing.T) {
	f := newRestore(t, mysqlRestore("s_shop_main", 0))
	var ae *contract.AgentError
	if _, err := Restore(context.Background(), f.e.d, f.re, restoreParams("db-s_shop_main", "s_shop_main", false)); !errors.As(err, &ae) || ae.Code != contract.ErrConflict {
		t.Errorf("into an existing database without overwrite: %v, want conflict", err)
	}
	if _, err := Restore(context.Background(), f.e.d, f.re, restoreParams("db-s_shop_main", "s_shop_none", true)); !errors.As(err, &ae) || ae.Code != contract.ErrNotFound {
		t.Errorf("overwriting a database the site doesn't list: %v, want not_found", err)
	}
	if len(mysqlCalls(f.e)) != 0 || len(f.cat.calls) != 0 {
		t.Fatal("a refused restore ran mysql or downloaded")
	}
	if _, err := Restore(context.Background(), f.e.d, f.re, restoreParams("db-s_shop_main", "s_shop_main", true)); err != nil {
		t.Fatal(err)
	}
	if len(mysqlCalls(f.e)) != 1 || len(f.created) != 0 || len(f.checked) != 0 {
		t.Errorf("overwrite: %d mysql runs, created %v", len(mysqlCalls(f.e)), f.created)
	}
}

// The part's hash is checked against the manifest before anything is
// written to MySQL or a home (R05-D10).
func TestRestoreHashMismatchWritesNothing(t *testing.T) {
	f := newRestore(t, mysqlRestore("s_shop_copy", 0))
	dir := "cat offsite:nightly/" + string(restoreID) + "/"
	f.cat.objects[dir+"db-s_shop_main.sql.gz"] = gz(t, []byte(goodDump+"-- tampered\n"))
	f.cat.objects[dir+"files-shop.tar.gz"] = append(f.cat.objects[dir+"files-shop.tar.gz"], 0)
	if _, err := Restore(context.Background(), f.e.d, f.re, restoreParams("db-s_shop_main", "s_shop_copy", false)); err == nil || !strings.Contains(err.Error(), "SHA-256") {
		t.Errorf("database: err = %v, want the hash mismatch", err)
	}
	if _, err := Restore(context.Background(), f.e.d, f.re, restoreParams("files-shop", "", false)); err == nil || !strings.Contains(err.Error(), "SHA-256") {
		t.Errorf("files: err = %v, want the hash mismatch", err)
	}
	if len(mysqlCalls(f.e)) != 0 || len(f.created) != 0 || len(f.h.mkdirs) != 0 || f.h.extracted != nil {
		t.Errorf("mysql %d, created %v, mkdir %v, extracted %v; want nothing", len(mysqlCalls(f.e)), f.created, f.h.mkdirs, f.h.extracted != nil)
	}
	spoolEmpty(t, f.e)
}

func tarEntries(t *testing.T, b []byte) map[string]string {
	t.Helper()
	zr, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(zr)
	out := map[string]string{}
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return out
		}
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(tr)
		out[h.Name] = h.Linkname + string(body)
	}
}

// Files go into restores/<backup> through the helper's extract-tar, never
// over the live site (OQ6). Links extract-tar refuses are left out and
// named, so the rest still restores.
func TestRestoreFilesIntoRestoreFolderThroughHelper(t *testing.T) {
	f := newRestore(t)
	res, err := Restore(context.Background(), f.e.d, f.re, restoreParams("files-shop", "", false))
	if err != nil {
		t.Fatal(err)
	}
	if res.Folder != "/home/s_shop/restores/"+string(restoreID) || res.Part != "files-shop" {
		t.Errorf("result %+v", res)
	}
	want := []string{"site/releases/initial/public-storage", "site/releases/initial/through", "site/releases/initial/up"}
	if !slices.Equal(res.SkippedLinks, want) {
		t.Errorf("skipped %q, want %q", res.SkippedLinks, want)
	}
	if !slices.Equal(f.h.mkdirs, []string{"restores"}) || f.h.extractDest != "restores/"+string(restoreID) || f.h.extractAs != "s_shop" {
		t.Errorf("mkdir %q, extracted into %q as %s", f.h.mkdirs, f.h.extractDest, f.h.extractAs)
	}
	got := tarEntries(t, f.h.extracted)
	for name, want := range map[string]string{
		"site/shared/.env": "APP_KEY=x\n", "site/releases/initial/index.html": "<h1>shop</h1>\n",
		"site/releases/initial/.env": "../../shared/.env", "site/releases/initial/storage": "../../shared/storage",
	} {
		if got[name] != want {
			t.Errorf("%s = %q, want %q", name, got[name], want)
		}
	}
	for _, name := range want {
		if _, ok := got[name]; ok {
			t.Errorf("%s reached extract-tar", name)
		}
	}
	if !strings.Contains(f.e.logs.String(), "site/releases/initial/up") {
		t.Error("the log doesn't name the skipped links")
	}
	spoolEmpty(t, f.e)

	// A folder already there is never written into.
	f.h.stats = map[string]helper.FileInfo{"restores/" + string(restoreID): {Mode: fs.ModeDir | 0o700}}
	var ae *contract.AgentError
	if _, err := Restore(context.Background(), f.e.d, f.re, restoreParams("files-shop", "", false)); !errors.As(err, &ae) || ae.Code != contract.ErrConflict {
		t.Errorf("into an existing folder: %v, want conflict", err)
	}
	// A failed extract leaves no half-made folder behind.
	f.h.stats, f.h.extractErr = nil, errors.New("as-user extract-tar as s_shop: no space left on device")
	if _, err := Restore(context.Background(), f.e.d, f.re, restoreParams("files-shop", "", false)); err == nil {
		t.Fatal("a failed extract passed")
	}
	if !slices.Contains(f.h.removed, "restores/"+string(restoreID)) {
		t.Errorf("removed %q, want the half-made folder", f.h.removed)
	}
}

// A database part restores only into the site whose database it was: a
// part of another site's would hand that site's data over.
func TestRestoreOtherSitesPartRefused(t *testing.T) {
	f := newRestore(t)
	dir := "offsite:nightly/" + string(restoreID) + "/"
	f.cat.objects["cat "+dir+"db-s_blog_main.sql.gz"] = gz(t, []byte(goodDump))
	_, err := Restore(context.Background(), f.e.d, f.re, restoreParams("db-s_blog_main", "s_shop_copy", false))
	var ae *contract.AgentError
	if !errors.As(err, &ae) || ae.Code != contract.ErrConflict || !strings.Contains(ae.Message, "blog") {
		t.Errorf("err = %v, want conflict naming blog", err)
	}
	if len(f.cat.calls) != 0 || len(f.created) != 0 {
		t.Error("a refused restore downloaded or created")
	}
	other := &contract.BackupRestoreParams{Plan: "nightly", Backup: restoreID, Part: "files-blog", Site: "shop"}
	if err := other.Validate(); err == nil {
		t.Error("another site's files part passed validation")
	}
}

// A restore into a new database that MySQL rejects halfway drops that
// database, so no half-restored copy stays.
func TestFailedRestoreDropsNewDatabase(t *testing.T) {
	f := newRestore(t, mysqlRestore("s_shop_copy", 1))
	_, err := Restore(context.Background(), f.e.d, f.re, restoreParams("db-s_shop_main", "s_shop_copy", false))
	if err == nil || !strings.Contains(err.Error(), "1064") {
		t.Fatalf("err = %v, want MySQL's error", err)
	}
	if !slices.Equal(f.dropped, []contract.DatabaseName{"s_shop_copy"}) {
		t.Errorf("dropped %v, want the new database", f.dropped)
	}
	// An overwrite never drops: the database was the site's before.
	g := newRestore(t, mysqlRestore("s_shop_main", 1))
	if _, err := Restore(context.Background(), g.e.d, g.re, restoreParams("db-s_shop_main", "s_shop_main", true)); err == nil {
		t.Fatal("a failed overwrite passed")
	}
	if len(g.dropped) != 0 {
		t.Errorf("an overwrite dropped %v", g.dropped)
	}
}
