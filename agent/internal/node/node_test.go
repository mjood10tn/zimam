package node

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"zimam/agent/contract"
	"zimam/agent/internal/paths"
	"zimam/agent/internal/php"
	"zimam/agent/internal/sysexec"
)

// keyServer answers NodeSource's key URL with key, and nothing else.
type keyServer struct {
	key  []byte
	hits int
}

func (k *keyServer) RoundTrip(r *http.Request) (*http.Response, error) {
	code, body := http.StatusNotFound, []byte("not found")
	if r.URL.String() == KeyURL {
		k.hits++
		code, body = http.StatusOK, k.key
	}
	return &http.Response{StatusCode: code, Status: http.StatusText(code), Body: io.NopCloser(bytes.NewReader(body)), Request: r}, nil
}

type testEnv struct {
	d    sysexec.Deps
	run  *sysexec.FakeRunner
	p    paths.Paths
	keys *keyServer
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// fixture is NodeSource's armored key as downloaded on 2026-11-18.
func fixture(t *testing.T) []byte { return mustRead(t, "testdata/nodesource.asc") }

func suryKey(t *testing.T) []byte { return mustRead(t, "../php/testdata/sury-apt.gpg") }

func newEnv(t *testing.T, key []byte) *testEnv {
	t.Helper()
	p := paths.Paths{Root: t.TempDir()}
	if err := os.MkdirAll(p.LocksDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	keys := &keyServer{key: key}
	run := sysexec.NewFakeRunner()
	return &testEnv{
		d:    sysexec.Deps{Paths: p, Run: run, HTTP: &http.Client{Transport: keys}, Logf: t.Logf, Now: func() time.Time { return time.Date(2026, 11, 18, 9, 0, 0, 0, time.UTC) }},
		run:  run,
		p:    p,
		keys: keys,
	}
}

const lockTimeout = "DPkg::Lock::Timeout=300"

var (
	aptUpdate    = sysexec.Script{Path: "/usr/bin/apt-get", Args: []string{"-o", lockTimeout, "update"}}
	installNode  = sysexec.Script{Path: "/usr/bin/apt-get", Args: []string{"-o", lockTimeout, "-o", "Dpkg::Options::=--force-confdef", "-o", "Dpkg::Options::=--force-confold", "install", "-y", "--no-install-recommends", "nodejs"}}
	purgeNode    = sysexec.Script{Path: "/usr/bin/apt-get", Args: []string{"-o", lockTimeout, "purge", "-y", "nodejs"}}
	queryScript  = sysexec.Script{Path: "/usr/bin/dpkg-query", Args: []string{"-W", "-f=${db:Status-Status} ${Version}", "nodejs"}}
	notInstalled = sysexec.Script{Path: queryScript.Path, Args: queryScript.Args, Result: sysexec.Result{ExitCode: 1, Stderr: []byte("dpkg-query: no packages found matching nodejs\n")}}
)

func line(s sysexec.Script) string { return strings.Join(append([]string{s.Path}, s.Args...), " ") }

// dpkgSays scripts the next dpkg-query answer.
func (e *testEnv) dpkgSays(status, version string) {
	s := queryScript
	s.Result = sysexec.Result{Stdout: []byte(status + " " + version)}
	e.run.Add(s)
}

func (e *testEnv) lines() []string {
	var out []string
	for _, c := range e.run.Calls() {
		out = append(out, strings.Join(append([]string{c.Path}, c.Args...), " "))
	}
	return out
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	return string(mustRead(t, path))
}

func TestRepoChecksPinnedFingerprint(t *testing.T) {
	if fpr, err := php.CheckSingleCertificate(fixture(t)); err != nil || fpr != Fingerprint {
		t.Fatalf("the fixture has %q, %v; want %s", fpr, err, Fingerprint)
	}
	other, err := php.CheckSingleCertificate(suryKey(t))
	if err != nil {
		t.Fatal(err)
	}
	e := newEnv(t, suryKey(t))
	err = EnsureRepo(context.Background(), e.d, "26")
	if err == nil || !strings.Contains(err.Error(), other) || !strings.Contains(err.Error(), Fingerprint) {
		t.Fatalf("EnsureRepo = %v, want an error naming both fingerprints", err)
	}
	for _, path := range []string{e.p.NodeKeyring(), e.p.NodeSources(), e.p.NodePin()} {
		if _, err := os.Lstat(path); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("%s was written (Lstat: %v)", path, err)
		}
	}
	if len(e.run.Calls()) != 0 {
		t.Errorf("ran %q", e.lines())
	}
}

// APT trusts every key in a Signed-By keyring, so a download with a second
// key behind NodeSource's is refused whole.
func TestRepoRefusesExtraKeyPackets(t *testing.T) {
	nodeBin, err := php.BinaryKeyring(fixture(t))
	if err != nil {
		t.Fatal(err)
	}
	e := newEnv(t, append(slices.Clone(nodeBin), suryKey(t)...))
	err = EnsureRepo(context.Background(), e.d, "26")
	if err == nil || !strings.Contains(err.Error(), "primary keys") {
		t.Fatalf("EnsureRepo = %v, want the extra primary key refused", err)
	}
	if _, err := os.Lstat(e.p.NodeKeyring()); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the keyring was written (Lstat: %v)", err)
	}
}

func TestEnsureRepoWritesBinaryKeyringSourcesAndPin(t *testing.T) {
	e := newEnv(t, fixture(t))
	e.run.Add(aptUpdate)
	if err := EnsureRepo(context.Background(), e.d, "24"); err != nil {
		t.Fatalf("EnsureRepo: %v", err)
	}
	k := readFile(t, e.p.NodeKeyring())
	if strings.HasPrefix(k, "-----BEGIN") {
		t.Fatal("the keyring was written armored; APT ignores that in a .gpg file")
	}
	if fpr, err := php.CheckSingleCertificate([]byte(k)); err != nil || fpr != Fingerprint {
		t.Errorf("the written keyring has %q, %v", fpr, err)
	}
	if src := readFile(t, e.p.NodeSources()); !strings.Contains(src, "URIs: https://deb.nodesource.com/node_24.x\n") || !strings.Contains(src, "Suites: nodistro\n") {
		t.Errorf("sources:\n%s", src)
	}
	if pin := readFile(t, e.p.NodePin()); strings.Count(pin, "Pin-Priority") != 3 {
		t.Errorf("pin:\n%s", pin)
	}
	if got := e.lines(); !slices.Equal(got, []string{line(aptUpdate)}) {
		t.Errorf("ran %q", got)
	}
}

func TestInstallSequence(t *testing.T) {
	e := newEnv(t, fixture(t))
	e.run.Add(notInstalled)
	e.run.Add(aptUpdate)
	e.run.Add(installNode)
	e.dpkgSays("installed", "26.13.1-1nodesource1")
	got, err := Install(context.Background(), e.d, "26")
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if got.Major != "26" || got.Version != "26.13.1-1nodesource1" {
		t.Errorf("result = %+v", got)
	}
	want := []string{line(queryScript), line(aptUpdate), line(installNode), line(queryScript)}
	if lines := e.lines(); !slices.Equal(lines, want) {
		t.Errorf("ran\n%s\nwant\n%s", strings.Join(lines, "\n"), strings.Join(want, "\n"))
	}
	for _, c := range e.run.Calls() {
		if c.Path == "/usr/bin/apt-get" && !slices.Contains(c.Env, "DEBIAN_FRONTEND=noninteractive") {
			t.Errorf("apt-get ran with env %q", c.Env)
		}
	}
}

func TestChangeMajorPurgesFirst(t *testing.T) {
	e := newEnv(t, fixture(t))
	e.dpkgSays("installed", "26.13.1-1nodesource1")
	e.run.Add(purgeNode)
	e.run.Add(aptUpdate)
	e.run.Add(installNode)
	e.dpkgSays("installed", "24.22.3-1nodesource1")
	got, err := Install(context.Background(), e.d, "24")
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if got.Major != "24" {
		t.Errorf("result = %+v", got)
	}
	want := []string{line(queryScript), line(aptUpdate), line(purgeNode), line(installNode), line(queryScript)}
	if lines := e.lines(); !slices.Equal(lines, want) {
		t.Errorf("ran\n%s\nwant\n%s", strings.Join(lines, "\n"), strings.Join(want, "\n"))
	}
	if src := readFile(t, e.p.NodeSources()); !strings.Contains(src, "node_24.x") {
		t.Errorf("sources:\n%s", src)
	}
}

// The new major's repository is set up before the old Node is purged, so a
// network or key failure leaves the old one working.
func TestChangeMajorKeepsOldNodeWhenRepoFails(t *testing.T) {
	e := newEnv(t, suryKey(t))
	e.dpkgSays("installed", "26.13.1-1nodesource1")
	e.run.Add(purgeNode)
	if _, err := Install(context.Background(), e.d, "24"); err == nil {
		t.Fatal("Install succeeded with the wrong key")
	}
	if lines := e.lines(); slices.Contains(lines, line(purgeNode)) {
		t.Errorf("purged the old Node although the new repository failed:\n%s", strings.Join(lines, "\n"))
	}
}

// R04-D19 Update: the same major is upgraded in place, never purged.
func TestSameMajorUpgradesInPlace(t *testing.T) {
	e := newEnv(t, fixture(t))
	e.dpkgSays("installed", "26.12.0-1nodesource1")
	e.run.Add(aptUpdate)
	e.run.Add(installNode)
	e.dpkgSays("installed", "26.13.1-1nodesource1")
	got, err := Install(context.Background(), e.d, "26")
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if got.Version != "26.13.1-1nodesource1" {
		t.Errorf("result = %+v", got)
	}
	want := []string{line(queryScript), line(aptUpdate), line(installNode), line(queryScript)}
	if lines := e.lines(); !slices.Equal(lines, want) {
		t.Errorf("ran\n%s\nwant\n%s", strings.Join(lines, "\n"), strings.Join(want, "\n"))
	}
}

// If the NodeSource repository fails, APT must not leave Ubuntu's own
// nodejs standing in for it (spike R04-D6): the version is checked.
func TestInstallRefusesAnotherSource(t *testing.T) {
	e := newEnv(t, fixture(t))
	e.run.Add(notInstalled)
	e.run.Add(aptUpdate)
	e.run.Add(installNode)
	e.dpkgSays("installed", "22.22.1+dfsg+~cs22.19.15-1ubuntu1")
	if _, err := Install(context.Background(), e.d, "26"); err == nil || !strings.Contains(err.Error(), "22.22.1") {
		t.Errorf("Install = %v, want the wrong version refused", err)
	}
}

func TestInstallRefusesUnknownMajor(t *testing.T) {
	e := newEnv(t, fixture(t))
	if _, err := Install(context.Background(), e.d, "22"); err == nil {
		t.Error("Install accepted major 22")
	}
}

func TestUninstallPurgesAndRemovesRepo(t *testing.T) {
	e := newEnv(t, fixture(t))
	for _, path := range []string{e.p.NodeKeyring(), e.p.NodeSources(), e.p.NodePin()} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	e.run.Add(purgeNode)
	if err := Uninstall(context.Background(), e.d); err != nil {
		t.Fatalf("Uninstall: %v", err)
	}
	for _, path := range []string{e.p.NodeKeyring(), e.p.NodeSources(), e.p.NodePin()} {
		if _, err := os.Lstat(path); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("%s is still there (Lstat: %v)", path, err)
		}
	}
	if got := e.lines(); !slices.Equal(got, []string{line(purgeNode)}) {
		t.Errorf("ran %q", got)
	}
}

func TestInstalled(t *testing.T) {
	for name, tc := range map[string]struct {
		script func(e *testEnv)
		want   string
	}{
		"not installed":       {func(e *testEnv) { e.run.Add(notInstalled) }, ""},
		"installed":           {func(e *testEnv) { e.dpkgSays("installed", "26.13.1-1nodesource1") }, "26.13.1-1nodesource1"},
		"removed, not purged": {func(e *testEnv) { e.dpkgSays("config-files", "26.13.1-1nodesource1") }, ""},
	} {
		e := newEnv(t, nil)
		tc.script(e)
		if v, err := Installed(context.Background(), e.d); err != nil || v != tc.want {
			t.Errorf("%s: %q, %v; want %q", name, v, err, tc.want)
		}
	}
	for in, want := range map[string]contract.NodeMajor{"26.13.1-1nodesource1": "26", "24.22.3-1nodesource1": "24", "": ""} {
		if got := MajorOf(in); got != want {
			t.Errorf("MajorOf(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRepoMajor(t *testing.T) {
	e := newEnv(t, fixture(t))
	if m, err := RepoMajor(e.p); err != nil || m != "" {
		t.Errorf("before the repo: %q, %v", m, err)
	}
	e.run.Add(aptUpdate)
	if err := EnsureRepo(context.Background(), e.d, "24"); err != nil {
		t.Fatal(err)
	}
	if m, err := RepoMajor(e.p); err != nil || m != "24" {
		t.Errorf("after: %q, %v", m, err)
	}
}

// Maintenance's refresh downloads the key again and keeps the major APT
// follows.
func TestRefreshRepoKeepsTheMajor(t *testing.T) {
	e := newEnv(t, fixture(t))
	e.run.Add(aptUpdate)
	if err := EnsureRepo(context.Background(), e.d, "24"); err != nil {
		t.Fatal(err)
	}
	if err := RefreshRepo(context.Background(), e.d); err != nil {
		t.Fatalf("RefreshRepo: %v", err)
	}
	if e.keys.hits != 2 || !strings.Contains(readFile(t, e.p.NodeSources()), "node_24.x") {
		t.Errorf("hits = %d, sources:\n%s", e.keys.hits, readFile(t, e.p.NodeSources()))
	}
}

// RefreshRepo reads the followed major under the repository lock: a
// node.install that switches it while the refresh waits must win.
func TestRefreshRepoReadsTheMajorUnderTheLock(t *testing.T) {
	e := newEnv(t, fixture(t))
	e.run.Add(aptUpdate)
	if err := EnsureRepo(context.Background(), e.d, "24"); err != nil {
		t.Fatal(err)
	}
	unlock, err := sysexec.Lock(context.Background(), e.p.AptRepoLock())
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- RefreshRepo(context.Background(), e.d) }()
	time.Sleep(150 * time.Millisecond)
	if err := os.WriteFile(e.p.NodeSources(), []byte("URIs: https://deb.nodesource.com/node_26.x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	unlock()
	if err := <-done; err != nil {
		t.Fatalf("RefreshRepo: %v", err)
	}
	if src := readFile(t, e.p.NodeSources()); !strings.Contains(src, "node_26.x") {
		t.Errorf("the refresh wrote back the major it read before the lock:\n%s", src)
	}
}
