package composer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"zimam/agent/internal/paths"
	"zimam/agent/internal/sysexec"
)

func sum(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// serve answers every request with body and counts the requests.
func serve(t *testing.T, body []byte) (*httptest.Server, *int) {
	t.Helper()
	hits := new(int)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*hits++
		w.Write(body)
	}))
	t.Cleanup(srv.Close)
	return srv, hits
}

func testDeps(t *testing.T, srv *httptest.Server) sysexec.Deps {
	t.Helper()
	return sysexec.Deps{
		Paths: paths.Paths{Root: t.TempDir()},
		Logf:  t.Logf,
		Now:   time.Now,
		HTTP:  srv.Client(),
	}
}

func testPin(srv *httptest.Server, content []byte) pin {
	return pin{version: "9.9.9", sha256: sum(content), url: srv.URL + "/download/9.9.9/composer.phar"}
}

// leftovers lists what is in the Composer directory besides the phar.
func leftovers(t *testing.T, d sysexec.Deps, phar string) []string {
	t.Helper()
	entries, err := os.ReadDir(d.Paths.ComposerDir())
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		if filepath.Join(d.Paths.ComposerDir(), e.Name()) != phar {
			out = append(out, e.Name())
		}
	}
	return out
}

func TestComposerPinIsWellFormed(t *testing.T) {
	if !regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`).MatchString(Version) {
		t.Errorf("Version %q is not x.y.z", Version)
	}
	if !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(SHA256) {
		t.Errorf("SHA256 %q is not 64 lowercase hex digits", SHA256)
	}
	// Spike amendment A9.
	if Version != "2.11.2" || SHA256 != "dd158c0afd41d15fac89e1b4e47b5d42911fe316c3687cd5edac98319b25d028" {
		t.Errorf("pin = %s %s, want A9's 2.11.2 and its SHA-256", Version, SHA256)
	}
	if want := "https://getcomposer.org/download/" + Version + "/composer.phar"; pinned.url != want {
		t.Errorf("url = %q, want %q", pinned.url, want)
	}
	if pinned.version != Version || pinned.sha256 != SHA256 {
		t.Errorf("pinned = %+v, want the constants", pinned)
	}
	if got, want := (paths.Paths{}).ComposerPhar(Version), "/usr/local/lib/zimam/composer/composer-2.11.2.phar"; got != want {
		t.Errorf("phar path = %q, want %q (R02-D6)", got, want)
	}
}

func TestComposerDownloadsAndVerifies(t *testing.T) {
	content := []byte("<?php // a phar")
	srv, hits := serve(t, content)
	d := testDeps(t, srv)
	p := testPin(srv, content)
	path, err := ensure(context.Background(), d, p)
	if err != nil {
		t.Fatal(err)
	}
	if want := d.Paths.ComposerPhar(p.version); path != want {
		t.Errorf("path = %q, want %q", path, want)
	}
	got, err := os.ReadFile(path)
	if err != nil || string(got) != string(content) {
		t.Fatalf("phar = %q, %v", got, err)
	}
	fi, _ := os.Stat(path)
	if fi.Mode().Perm() != 0o644 {
		t.Errorf("phar mode %o, want 644", fi.Mode().Perm())
	}
	di, _ := os.Stat(d.Paths.ComposerDir())
	if di.Mode().Perm() != 0o755 {
		t.Errorf("dir mode %o, want 755", di.Mode().Perm())
	}
	if *hits != 1 {
		t.Errorf("%d downloads, want 1", *hits)
	}
	if l := leftovers(t, d, path); len(l) != 0 {
		t.Errorf("left behind: %v", l)
	}
}

func TestComposerRefusesWrongHash(t *testing.T) {
	srv, _ := serve(t, []byte("not the pinned phar"))
	d := testDeps(t, srv)
	p := testPin(srv, []byte("the pinned phar"))
	_, err := ensure(context.Background(), d, p)
	if err == nil || !strings.Contains(err.Error(), "SHA-256") {
		t.Fatalf("err = %v, want a SHA-256 mismatch", err)
	}
	phar := d.Paths.ComposerPhar(p.version)
	if _, err := os.Stat(phar); !os.IsNotExist(err) {
		t.Errorf("%s exists after a wrong hash: %v", phar, err)
	}
	if l := leftovers(t, d, phar); len(l) != 0 {
		t.Errorf("left behind: %v", l)
	}
}

func TestComposerReusesVerifiedPhar(t *testing.T) {
	content := []byte("<?php // already here")
	srv, hits := serve(t, content)
	d := testDeps(t, srv)
	p := testPin(srv, content)
	phar := d.Paths.ComposerPhar(p.version)
	if err := os.MkdirAll(filepath.Dir(phar), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(phar, content, 0o644); err != nil {
		t.Fatal(err)
	}
	path, err := ensure(context.Background(), d, p)
	if err != nil || path != phar {
		t.Fatalf("ensure = %q, %v; want %q", path, err, phar)
	}
	if *hits != 0 {
		t.Errorf("%d downloads, want none for a verified phar", *hits)
	}
}

func TestComposerReplacesTamperedPhar(t *testing.T) {
	content := []byte("<?php // the real one")
	srv, hits := serve(t, content)
	d := testDeps(t, srv)
	p := testPin(srv, content)
	phar := d.Paths.ComposerPhar(p.version)
	if err := os.MkdirAll(filepath.Dir(phar), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(phar, []byte("<?php // changed"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ensure(context.Background(), d, p); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(phar)
	if string(got) != string(content) || *hits != 1 {
		t.Errorf("phar = %q after %d downloads; want the pinned content after 1", got, *hits)
	}
}

func TestComposerRefusesBadStatus(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "gone", http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	d := testDeps(t, srv)
	p := testPin(srv, []byte("gone\n"))
	if _, err := ensure(context.Background(), d, p); err == nil {
		t.Fatal("a 404 whose body happens to match the pin was accepted")
	}
}

func TestComposerRefusesOversizedDownload(t *testing.T) {
	big := make([]byte, maxPharBytes+1)
	srv, _ := serve(t, big)
	d := testDeps(t, srv)
	p := testPin(srv, big)
	if _, err := ensure(context.Background(), d, p); err == nil {
		t.Fatal("an oversized phar was accepted")
	}
	if l := leftovers(t, d, d.Paths.ComposerPhar(p.version)); len(l) != 0 {
		t.Errorf("left behind: %v", l)
	}
}
