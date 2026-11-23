package backup

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"zimam/agent/contract"
	"zimam/agent/internal/helper"
	"zimam/agent/internal/sysexec"
)

const (
	testDst   = "offsite:nightly/20261123T033000Z/db-s_shop_main.sql.gz"
	goodDump  = "-- MySQL dump 10.13\nINSERT INTO t VALUES (1);\n-- Dump completed on 2026-11-23  3:30:01\n"
	shortDump = "-- MySQL dump 10.13\nINSERT INTO t VALUES (1"
)

var wantDumpArgs = []string{"--protocol=socket", "--user=root", "--single-transaction", "--quick", "--routines", "--events",
	"--triggers", "--no-tablespaces", "s_shop_main"}

func dumpScript(stdout string, exit int) sysexec.Script {
	return sysexec.Script{Path: mysqldumpPath, Args: wantDumpArgs, Result: sysexec.Result{Stdout: []byte(stdout), ExitCode: exit}}
}

func gunzip(t *testing.T, b []byte) string {
	t.Helper()
	r, err := gzip.NewReader(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

// A dump that exits non-zero fails its part though rclone stored what it
// got: stdout bytes are never proof (Task 1, a missing database).
func TestDumpFailureFailsPartEvenIfUploadSucceeded(t *testing.T) {
	e := newEnv(t, dumpScript("-- MySQL dump 10.13\n", 2))
	_, err := Pipe(context.Background(), e.d, DatabaseSource(e.d, "s_shop_main"), testDst)
	if err == nil || !strings.Contains(err.Error(), "db-s_shop_main") {
		t.Fatalf("err = %v, want the part's failure", err)
	}
	if got := e.rclone.commands(); !slices.Equal(got, []string{"rcat", "deletefile"}) {
		t.Errorf("rclone ran %v, want rcat then deletefile", got)
	}
}

// A dump that exits 0 without mysqldump's last line stopped early.
func TestDumpWithoutTrailerFailsPart(t *testing.T) {
	e := newEnv(t, dumpScript(shortDump, 0))
	_, err := Pipe(context.Background(), e.d, DatabaseSource(e.d, "s_shop_main"), testDst)
	if err == nil || !strings.Contains(err.Error(), "-- Dump completed") {
		t.Fatalf("err = %v, want the missing trailer", err)
	}
}

func TestUploadFailureFailsPart(t *testing.T) {
	e := newEnv(t, dumpScript(goodDump, 0))
	e.rclone.answer = func(args []string) (sysexec.Result, error) {
		if args[0] == "rcat" {
			return sysexec.Result{ExitCode: 1}, &sysexec.ExitError{Path: "rclone", Code: 1, Stderr: []byte("permission denied")}
		}
		return sysexec.Result{}, nil
	}
	_, err := Pipe(context.Background(), e.d, DatabaseSource(e.d, "s_shop_main"), testDst)
	if err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("err = %v, want rclone's failure", err)
	}
}

// rclone that stops reading and exits 0 leaves gzip's last write with
// nowhere to go: a stage error fails the part though both processes passed.
func TestGzipErrorFailsPart(t *testing.T) {
	e := newEnv(t, dumpScript(goodDump, 0))
	var calls []string
	rcloneRun = func(_ context.Context, _ sysexec.Deps, stdin io.Reader, args ...string) (sysexec.Result, error) {
		calls = append(calls, args[0])
		if stdin != nil {
			io.ReadFull(stdin, make([]byte, 10)) // the gzip header only
		}
		return sysexec.Result{}, nil
	}
	_, err := Pipe(context.Background(), e.d, DatabaseSource(e.d, "s_shop_main"), testDst)
	if err == nil {
		t.Fatal("a stream rclone didn't take whole passed")
	}
	if !slices.Equal(calls, []string{"rcat", "deletefile"}) {
		t.Errorf("rclone ran %v, want rcat then deletefile", calls)
	}
}

func TestPartialObjectDeletedOnFailure(t *testing.T) {
	e := newEnv(t, dumpScript(shortDump, 0))
	Pipe(context.Background(), e.d, DatabaseSource(e.d, "s_shop_main"), testDst)
	if len(e.rclone.calls) != 2 || !slices.Equal(e.rclone.calls[1], []string{"deletefile", testDst}) {
		t.Fatalf("rclone calls %q, want deletefile %s last", e.rclone.calls, testDst)
	}
	if !strings.Contains(e.logs.String(), "Deleted the incomplete "+testDst) {
		t.Errorf("log %q doesn't say the object was deleted", e.logs.String())
	}

	ok := newEnv(t, dumpScript(goodDump, 0))
	if _, err := Pipe(context.Background(), ok.d, DatabaseSource(ok.d, "s_shop_main"), testDst); err != nil {
		t.Fatal(err)
	}
	if got := ok.rclone.commands(); !slices.Equal(got, []string{"rcat"}) {
		t.Errorf("a passed part ran %v, want rcat alone", got)
	}
}

func TestHashAndSizeRecorded(t *testing.T) {
	e := newEnv(t, dumpScript(goodDump, 0))
	info, err := Pipe(context.Background(), e.d, DatabaseSource(e.d, "s_shop_main"), testDst)
	if err != nil {
		t.Fatal(err)
	}
	up := []byte(e.rclone.stdins[0])
	sum := sha256.Sum256(up)
	if info.Part != "db-s_shop_main" || int(info.Bytes) != len(up) || info.SHA256 != hex.EncodeToString(sum[:]) {
		t.Errorf("info = %+v, want db-s_shop_main, %d bytes, %x", info, len(up), sum)
	}
	if gunzip(t, up) != goodDump {
		t.Error("the upload isn't the gzipped dump")
	}
	if !slices.Equal(e.rclone.calls[0], []string{"rcat", testDst}) {
		t.Errorf("rcat args %q", e.rclone.calls[0])
	}
}

// fakeHelper is the helper for the files and panel parts.
type fakeHelper struct {
	mu       sync.Mutex
	data     string
	err      error
	stats    map[string]helper.FileInfo
	archived [][]string
	removed  []string
	order    *[]string // shared with a runner, to check the order of steps
}

func (f *fakeHelper) ArchiveTree(_ context.Context, a helper.Account, w io.Writer, paths ...string) error {
	f.mu.Lock()
	f.archived = append(f.archived, append([]string{a.Name}, paths...))
	if f.order != nil {
		*f.order = append(*f.order, "archive")
	}
	f.mu.Unlock()
	if _, err := io.WriteString(w, f.data); err != nil {
		return err
	}
	return f.err
}

func (f *fakeHelper) Stat(_ context.Context, _ helper.Account, name string) (helper.FileInfo, error) {
	fi, ok := f.stats[name]
	if !ok {
		return fi, fs.ErrNotExist
	}
	return fi, nil
}

func (f *fakeHelper) RemoveTree(_ context.Context, _ helper.Account, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removed = append(f.removed, name)
	if f.order != nil {
		*f.order = append(*f.order, "remove")
	}
	return nil
}

var shopAccount = helper.Account{Name: "s_shop", UID: 1001, GID: 1001, Home: "/home/s_shop"}

// A helper that fails after writing part of the archive fails the part.
func TestArchiveFailureFailsPart(t *testing.T) {
	e := newEnv(t)
	h := &fakeHelper{data: "partial tar", err: errors.New("as-user archive-tree as s_shop: disk error")}
	dst := "offsite:nightly/20261123T033000Z/files-shop.tar.gz"
	_, err := Pipe(context.Background(), e.d, ArchiveSource(h, shopAccount, "files-shop", "site/shared"), dst)
	if err == nil || !strings.Contains(err.Error(), "disk error") {
		t.Fatalf("err = %v, want the helper's failure", err)
	}
	if got := e.rclone.commands(); !slices.Equal(got, []string{"rcat", "deletefile"}) {
		t.Errorf("rclone ran %v", got)
	}
	ok := &fakeHelper{data: "whole tar"}
	info, err := Pipe(context.Background(), e.d, ArchiveSource(ok, shopAccount, "files-shop", "site/shared"), dst)
	if err != nil || e.rclone.stdins[2] != "whole tar" || info.Bytes != 9 {
		t.Errorf("a passed archive: %+v, %v, uploaded %q (the helper's gzip goes up as it is)", info, err, e.rclone.stdins[2])
	}
}

// A site's files are its shared folder, when it has one, and the active
// release under its real name, never through site/current (Task 5).
func TestSiteFilesNamesActiveRelease(t *testing.T) {
	link := func(target string) helper.FileInfo {
		return helper.FileInfo{Mode: fs.ModeSymlink | 0o777, Target: target}
	}
	dir := helper.FileInfo{Mode: fs.ModeDir | 0o750}
	h := &fakeHelper{stats: map[string]helper.FileInfo{"site/current": link("releases/20261123033000-abc1234"), "site/shared": dir}}
	got, err := SiteFiles(context.Background(), h, shopAccount)
	if err != nil || !slices.Equal(got, []string{"site/shared", "site/releases/20261123033000-abc1234"}) {
		t.Errorf("SiteFiles = %q, %v", got, err)
	}
	h = &fakeHelper{stats: map[string]helper.FileInfo{"site/current": link("releases/initial")}}
	if got, err := SiteFiles(context.Background(), h, shopAccount); err != nil || !slices.Equal(got, []string{"site/releases/initial"}) {
		t.Errorf("without shared: %q, %v", got, err)
	}
	for _, target := range []string{"/etc", "../x", "releases/../../x", "releases/a/b", "releases/", "releases/.."} {
		h = &fakeHelper{stats: map[string]helper.FileInfo{"site/current": link(target)}}
		if got, err := SiteFiles(context.Background(), h, shopAccount); err == nil {
			t.Errorf("target %q gave %q, want a refusal", target, got)
		}
	}
	h = &fakeHelper{stats: map[string]helper.FileInfo{"site/current": dir}}
	if _, err := SiteFiles(context.Background(), h, shopAccount); err == nil {
		t.Error("a site/current that isn't a link passed")
	}
}

// The panel part snapshots the panel's database as zimam, archives the
// snapshot, and removes it again (R05-D5).
func TestPanelSourceSnapshotsArchivesAndRemoves(t *testing.T) {
	e := newEnv(t)
	snap := e.d.Paths.PanelSharedDir() + "/zimam-backup.sqlite"
	e.fake.Add(sysexec.Script{Path: "/usr/sbin/runuser", Args: []string{"-u", "zimam", "--", "/usr/bin/php8.4", e.d.Paths.PanelCurrent() + "/artisan", "zimam:snapshot-db", snap}})
	e.d.Config.PanelPHP = "8.4"
	zimam := helper.Account{Name: "zimam", UID: 999, GID: 999, Home: "/home/zimam"}
	h := &fakeHelper{data: "panel tar"}
	if _, err := Pipe(context.Background(), e.d, PanelSource(e.d, h, zimam), "offsite:nightly/20261123T033000Z/panel.tar.gz"); err != nil {
		t.Fatal(err)
	}
	if len(h.archived) != 1 || !slices.Equal(h.archived[0], []string{"zimam", "panel/shared/zimam-backup.sqlite"}) {
		t.Errorf("archived %q", h.archived)
	}
	if !slices.Equal(h.removed, []string{"panel/shared/zimam-backup.sqlite", "panel/shared/zimam-backup.sqlite"}) {
		t.Errorf("removed %q, want the snapshot before (a killed run's) and after", h.removed)
	}
}

func TestObjectNames(t *testing.T) {
	for part, want := range map[contract.BackupPart]string{"db-s_shop_main": "db-s_shop_main.sql.gz", "files-shop": "files-shop.tar.gz", "panel": "panel.tar.gz"} {
		if got := ObjectName(part); got != want {
			t.Errorf("ObjectName(%s) = %s, want %s", part, got, want)
		}
	}
}

func TestManifestUploadedLast(t *testing.T) {
	e := newEnv(t, dumpScript(goodDump, 0))
	h := &fakeHelper{data: "tar"}
	dir := "offsite:nightly/20261123T033000Z/"
	a, err := Pipe(context.Background(), e.d, DatabaseSource(e.d, "s_shop_main"), dir+"db-s_shop_main.sql.gz")
	if err != nil {
		t.Fatal(err)
	}
	b, err := Pipe(context.Background(), e.d, ArchiveSource(h, shopAccount, "files-shop", "site/shared"), dir+"files-shop.tar.gz")
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 11, 23, 3, 30, 0, 0, time.UTC)
	parts := []contract.BackupPartInfo{a, b}
	if err := WriteManifest(context.Background(), e.d, "offsite", "nightly", "20261123T033000Z", start, parts); err != nil {
		t.Fatal(err)
	}
	last := e.rclone.calls[len(e.rclone.calls)-1]
	if !slices.Equal(last, []string{"rcat", dir + "manifest.json"}) {
		t.Fatalf("last rclone call %q, want the manifest", last)
	}
	var m Manifest
	if err := contract.DecodeStrict([]byte(e.rclone.stdins[len(e.rclone.stdins)-1]), &m); err != nil {
		t.Fatal(err)
	}
	if m.Plan != "nightly" || m.Backup != "20261123T033000Z" || !m.StartedAt.Equal(start) || !m.FinishedAt.Equal(e.d.Now()) ||
		len(m.Parts) != 2 || m.Parts[0] != a || m.Parts[1] != b || m.RcloneVersion == "" || m.AgentVersion == "" {
		t.Errorf("manifest = %+v", m)
	}
}

// Cancelling a run stops the dump and the upload, and the part fails.
func TestCancelKillsBothEnds(t *testing.T) {
	e := newEnv(t)
	var dumpStopped, uploadStopped atomic.Bool
	e.d.Run = runnerFunc(func(ctx context.Context, c sysexec.Cmd) (sysexec.Result, error) {
		c.Stdout.Write([]byte("-- MySQL dump\n"))
		<-ctx.Done()
		dumpStopped.Store(true)
		return sysexec.Result{}, ctx.Err()
	})
	rcloneRun = func(ctx context.Context, _ sysexec.Deps, stdin io.Reader, args ...string) (sysexec.Result, error) {
		if args[0] != "rcat" {
			return sysexec.Result{}, nil
		}
		go io.Copy(io.Discard, stdin)
		<-ctx.Done()
		uploadStopped.Store(true)
		return sysexec.Result{}, ctx.Err()
	}
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel)
	done := make(chan error)
	go func() {
		_, err := Pipe(ctx, e.d, DatabaseSource(e.d, "s_shop_main"), testDst)
		done <- err
	}()
	select {
	case err := <-done:
		if err == nil || !dumpStopped.Load() || !uploadStopped.Load() {
			t.Errorf("err %v, dump stopped %v, upload stopped %v", err, dumpStopped.Load(), uploadStopped.Load())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Pipe didn't return after cancel")
	}
}

type runnerFunc func(ctx context.Context, c sysexec.Cmd) (sysexec.Result, error)

func (f runnerFunc) Run(ctx context.Context, c sysexec.Cmd) (sysexec.Result, error) { return f(ctx, c) }

// A 2 GiB dump passes through with the heap staying small: nothing in the
// pipeline holds the stream.
func TestPipelineStreamsWithoutBuffering(t *testing.T) {
	if testing.Short() {
		t.Skip("streams 2 GiB")
	}
	const total = 2 << 30
	e := newEnv(t)
	line := bytes.Repeat([]byte("INSERT INTO t VALUES (1,'zimam backup stream test');\n"), 1300)
	e.d.Run = runnerFunc(func(ctx context.Context, c sysexec.Cmd) (sysexec.Result, error) {
		for n := 0; n < total; n += len(line) {
			if _, err := c.Stdout.Write(line); err != nil {
				return sysexec.Result{}, err
			}
		}
		_, err := c.Stdout.Write([]byte("-- Dump completed on 2026-11-23  3:30:01\n"))
		return sysexec.Result{}, err
	})
	var uploaded int64
	rcloneRun = func(_ context.Context, _ sysexec.Deps, stdin io.Reader, args ...string) (sysexec.Result, error) {
		n, err := io.Copy(io.Discard, stdin)
		uploaded = n
		return sysexec.Result{}, err
	}
	runtime.GC()
	var base runtime.MemStats
	runtime.ReadMemStats(&base)
	var peak atomic.Uint64
	stop := make(chan struct{})
	go func() {
		var m runtime.MemStats
		for {
			select {
			case <-stop:
				return
			case <-time.After(20 * time.Millisecond):
				runtime.ReadMemStats(&m)
				if m.HeapAlloc > peak.Load() {
					peak.Store(m.HeapAlloc)
				}
			}
		}
	}()
	info, err := Pipe(context.Background(), e.d, DatabaseSource(e.d, "s_shop_main"), testDst)
	close(stop)
	if err != nil {
		t.Fatal(err)
	}
	if int64(info.Bytes) != uploaded || uploaded == 0 {
		t.Errorf("recorded %d bytes, uploaded %d", info.Bytes, uploaded)
	}
	if growth := int64(peak.Load()) - int64(base.HeapAlloc); growth > 64<<20 {
		t.Errorf("heap grew %d MiB, want under 64", growth>>20)
	}
}

// The manifest is JSON a list can read back strictly.
func TestManifestIsStrictJSON(t *testing.T) {
	b, _ := json.Marshal(Manifest{})
	var m Manifest
	if err := contract.DecodeStrict(b, &m); err != nil {
		t.Fatal(err)
	}
}
