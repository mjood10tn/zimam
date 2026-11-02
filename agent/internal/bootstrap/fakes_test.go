package bootstrap

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"io/fs"
	"math/big"
	"net"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"zimam/agent/internal/audit"
	"zimam/agent/internal/helper"
	"zimam/agent/internal/paths"
	"zimam/agent/internal/release"
	"zimam/agent/internal/sysexec"
)

const (
	testVersion = "0.1.0"
	testBaseURL = "https://releases.example.com/zimam/releases"
	testDomain  = "panel.example.com"
	testLink    = "https://panel.example.com/setup/abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQ"
)

var testNow = time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)

// world is a server after install.sh's steps 1–5, under a temporary root:
// Nginx installed with its default site, the zimam account, and a release
// served over a fake transport. Every runner and helper call lands in
// events, in order.
type world struct {
	t      *testing.T
	p      paths.Paths
	sys    *fakeSystem
	help   *fakeHelper
	http   *fakeTransport
	b      *Bootstrap
	priv   ed25519.PrivateKey // signs the served release
	logs   *bytes.Buffer
	mu     *sync.Mutex
	events *[]string
}

func newWorld(t *testing.T) *world {
	t.Helper()
	p := paths.Paths{Root: t.TempDir()}
	for _, dir := range []string{
		"etc/nginx/sites-available", "etc/nginx/sites-enabled", "etc/systemd/system",
		"etc/zimam", "usr/sbin", "var/www/html", "var/lib", "var/log", "run",
	} {
		mustMkdir(t, filepath.Join(p.Root, dir))
	}
	mustWrite(t, filepath.Join(p.Root, "etc/nginx/sites-available/default"), "server { listen 80 default_server; }\n")
	if err := os.Symlink("/etc/nginx/sites-available/default", p.NginxDistDefault()); err != nil {
		t.Fatal(err)
	}

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	mu, events, logs := &sync.Mutex{}, &[]string{}, &bytes.Buffer{}
	w := &world{t: t, p: p, logs: logs, mu: mu, events: events}
	w.sys = &fakeSystem{w: w}
	w.help = &fakeHelper{w: w, nodes: map[string]*node{".": {dir: true, mode: 0o750}}}
	w.http = &fakeTransport{files: map[string][]byte{}}
	keyring, err := os.ReadFile("../php/testdata/sury-apt.gpg")
	if err != nil {
		t.Fatal(err)
	}
	w.http.files["https://packages.sury.org/php/apt.gpg"] = keyring
	w.priv = priv
	w.publish(priv, panelTarball(t), nil)

	d := sysexec.Deps{
		Paths: p,
		Run:   w.sys,
		Logf: func(format string, args ...any) {
			mu.Lock()
			defer mu.Unlock()
			fmt.Fprintf(logs, format+"\n", args...)
		},
		Now:  func() time.Time { return testNow },
		HTTP: &http.Client{Transport: w.http},
		DNS:  fakeResolver{},
	}
	w.b = &Bootstrap{
		Deps:         d,
		Helper:       w.help,
		Audit:        audit.Writer{Paths: p},
		Zimam:        helper.Account{Name: "zimam", UID: 999, GID: 999, Groups: []uint32{999, 998}, Home: "/home/zimam"},
		IDs:          IDs{RootUID: os.Getuid(), RootGID: os.Getgid(), AgentGID: os.Getgid()},
		PublicKey:    pub,
		BaseURL:      testBaseURL,
		AgentVersion: testVersion,
	}
	return w
}

// publish serves a release signed by priv with the given tarball. edit, if
// set, changes the manifest before it is signed.
func (w *world) publish(priv ed25519.PrivateKey, tarball []byte, edit func(*release.Manifest)) {
	w.t.Helper()
	sum := sha256.Sum256(tarball)
	m := release.Manifest{
		Version:         testVersion,
		Protocol:        1,
		MinAgentVersion: testVersion,
		PanelPHPMin:     "8.3",
		Expires:         testNow.AddDate(0, 0, 30),
		Artifacts:       map[string]string{"zimam-panel-" + testVersion + ".tar.gz": "sha256:" + hex.EncodeToString(sum[:])},
	}
	if edit != nil {
		edit(&m)
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		w.t.Fatal(err)
	}
	dir := testBaseURL + "/download/v" + testVersion + "/"
	w.http.files[dir+"manifest.json"] = data
	w.http.files[dir+"manifest.json.sig"] = ed25519.Sign(priv, data)
	w.http.files[dir+"zimam-panel-"+testVersion+".tar.gz"] = tarball
}

func opts() Options { return Options{PanelDomain: testDomain, Version: testVersion} }

func (w *world) event(e string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	*w.events = append(*w.events, e)
}

func (w *world) eventsSince(n int) []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return slices.Clone((*w.events)[n:])
}

func (w *world) eventCount() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(*w.events)
}

func (w *world) mustRun(o Options) string {
	w.t.Helper()
	link, err := w.b.Run(context.Background(), o)
	if err != nil {
		w.t.Fatalf("bootstrap: %v", err)
	}
	return link
}

// auditEntries reads audit.log.
func (w *world) auditEntries() []map[string]any {
	w.t.Helper()
	data, err := os.ReadFile(w.p.AuditLog())
	if err != nil {
		w.t.Fatal(err)
	}
	var entries []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var e map[string]any
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			w.t.Fatalf("audit line %q: %v", line, err)
		}
		entries = append(entries, e)
	}
	return entries
}

// auditedSteps lists the step of each bootstrap entry from the n-th on,
// with its outcome when it isn't ok.
func (w *world) auditedSteps(from int) []string {
	w.t.Helper()
	var steps []string
	for _, e := range w.auditEntries()[from:] {
		params, _ := e["params"].(map[string]any)
		s, _ := params["step"].(string)
		if e["outcome"] != "ok" {
			s += " " + e["outcome"].(string)
		}
		steps = append(steps, s)
	}
	return steps
}

// fakeSystem is the server's programs: it records every command and
// answers it, making the files a real run would leave. fail, when set,
// answers a command first.
type fakeSystem struct {
	w    *world
	mu   sync.Mutex
	cmds []sysexec.Cmd
	fail func(c sysexec.Cmd) error
}

var fpmPackage = regexp.MustCompile(`^php(\d\.\d)-fpm$`)

func (f *fakeSystem) Run(ctx context.Context, c sysexec.Cmd) (sysexec.Result, error) {
	if c.Stdin != nil {
		b, _ := io.ReadAll(c.Stdin)
		c.Stdin = bytes.NewReader(b)
	}
	f.mu.Lock()
	f.cmds = append(f.cmds, c)
	fail := f.fail
	f.mu.Unlock()
	f.w.event("run " + c.Path + " " + strings.Join(c.Args, " "))
	if fail != nil {
		if err := fail(c); err != nil {
			return sysexec.Result{ExitCode: 1}, err
		}
	}
	p := f.w.p
	t := f.w.t
	switch {
	case c.Path == "/usr/bin/apt-get" && slices.Contains(c.Args, "install"):
		for _, a := range c.Args {
			if m := fpmPackage.FindStringSubmatch(a); m != nil {
				v := phpV(m[1])
				mustWrite(t, p.PHPFPMBinary(v), "")
				mustMkdir(t, filepath.Dir(p.PHPIni(v)))
				mustMkdir(t, p.PHPPoolDir(v))
				mustWrite(t, p.PHPWwwPool(v), "[www]\n")
			}
		}
	case c.Path == "/usr/bin/update-alternatives" && c.Args[0] == "--set":
		link := p.Alternative(c.Args[1])
		mustMkdir(t, filepath.Dir(link))
		os.Remove(link)
		if err := os.Symlink(c.Args[2], link); err != nil {
			t.Fatal(err)
		}
	case c.Path == "/usr/bin/update-alternatives" && c.Args[0] == "--query":
		return sysexec.Result{ExitCode: 2}, &sysexec.ExitError{Path: c.Path, Code: 2}
	case c.Path == "/usr/bin/certbot" && !slices.Contains(c.Args, "--dry-run"):
		writeCert(t, p.LetsEncryptCert(c.Args[slices.Index(c.Args, "--cert-name")+1]))
	case c.Path == "/usr/sbin/runuser" && slices.Contains(c.Args, "zimam:setup-link"):
		return sysexec.Result{Stdout: []byte(testLink + "\nPanel IP allowlist: empty\n")}, nil
	}
	return sysexec.Result{}, nil
}

func (f *fakeSystem) commands() []sysexec.Cmd {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.cmds)
}

// argv is a command as one string.
func argv(c sysexec.Cmd) string { return c.Path + " " + strings.Join(c.Args, " ") }

// node is a file, directory or symlink in fakeHelper's home.
type node struct {
	dir     bool
	mode    fs.FileMode
	target  string // a symlink's
	content []byte
}

// fakeHelper is the helper with zimam's home in memory. Names are
// relative to the home, as the real helper's are.
type fakeHelper struct {
	w           *world
	mu          sync.Mutex
	nodes       map[string]*node
	writes      map[string][]byte
	limits      []helper.ExtractLimits
	accounts    []string
	failExtract error
}

func (h *fakeHelper) record(a helper.Account, op string, args ...string) {
	h.mu.Lock()
	h.accounts = append(h.accounts, a.Name)
	h.mu.Unlock()
	h.w.event("helper " + op + " " + strings.Join(args, " "))
}

func (h *fakeHelper) parentExists(name string) error {
	if dir := path.Dir(name); h.nodes[dir] == nil || !h.nodes[dir].dir {
		return fmt.Errorf("fake helper: %s has no parent directory: %w", name, fs.ErrNotExist)
	}
	return nil
}

func (h *fakeHelper) Mkdir(ctx context.Context, a helper.Account, name string, mode fs.FileMode) error {
	h.record(a, "mkdir", name, fmt.Sprintf("%o", mode))
	h.mu.Lock()
	defer h.mu.Unlock()
	for dir := name; dir != "."; dir = path.Dir(dir) {
		if n := h.nodes[dir]; n != nil && !n.dir {
			return fmt.Errorf("fake helper: %s is not a directory", dir)
		}
		if h.nodes[dir] == nil {
			h.nodes[dir] = &node{dir: true, mode: 0o750}
		}
	}
	h.nodes[name].mode = mode
	return nil
}

func (h *fakeHelper) Write(ctx context.Context, a helper.Account, name string, mode fs.FileMode, content io.Reader) error {
	b, err := io.ReadAll(content)
	if err != nil {
		return err
	}
	h.record(a, "write", name, fmt.Sprintf("%o", mode))
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := h.parentExists(name); err != nil {
		return err
	}
	h.nodes[name] = &node{mode: mode, content: b}
	if h.writes == nil {
		h.writes = map[string][]byte{}
	}
	h.writes[name] = b
	return nil
}

func (h *fakeHelper) Stat(ctx context.Context, a helper.Account, name string) (helper.FileInfo, error) {
	h.record(a, "stat", name)
	h.mu.Lock()
	defer h.mu.Unlock()
	n := h.nodes[path.Clean(name)]
	if n == nil {
		return helper.FileInfo{}, fmt.Errorf("as-user stat as zimam: %s: %w", name, fs.ErrNotExist)
	}
	fi := helper.FileInfo{Name: path.Base(name), Mode: n.mode, Size: int64(len(n.content)), Target: n.target}
	switch {
	case n.dir:
		fi.Mode |= fs.ModeDir
	case n.target != "":
		fi.Mode = fs.ModeSymlink | 0o777
	}
	return fi, nil
}

func (h *fakeHelper) SymlinkSwap(ctx context.Context, a helper.Account, link, target string) error {
	h.record(a, "symlink-swap", link, target)
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := h.parentExists(link); err != nil {
		return err
	}
	if n := h.nodes[link]; n != nil && n.dir {
		return fmt.Errorf("fake helper: rename over the directory %s", link)
	}
	h.nodes[link] = &node{target: target}
	return nil
}

func (h *fakeHelper) RemoveTree(ctx context.Context, a helper.Account, name string) error {
	h.record(a, "remove-tree", name)
	h.mu.Lock()
	defer h.mu.Unlock()
	for k := range h.nodes {
		if k == name || strings.HasPrefix(k, name+"/") {
			delete(h.nodes, k)
		}
	}
	return nil
}

func (h *fakeHelper) ExtractTar(ctx context.Context, a helper.Account, dest string, tarball io.Reader, lim helper.ExtractLimits) error {
	h.record(a, "extract-tar", dest)
	h.mu.Lock()
	defer h.mu.Unlock()
	h.limits = append(h.limits, lim)
	if h.failExtract != nil {
		return h.failExtract
	}
	if err := h.parentExists(dest); err != nil {
		return err
	}
	if h.nodes[dest] != nil {
		return fmt.Errorf("fake helper: %s already exists, and extract-tar opens files with O_EXCL", dest)
	}
	h.nodes[dest] = &node{dir: true, mode: 0o750}
	gz, err := gzip.NewReader(tarball)
	if err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		name := path.Join(dest, hdr.Name)
		if hdr.Typeflag == tar.TypeDir {
			h.nodes[name] = &node{dir: true, mode: 0o750}
			continue
		}
		b, _ := io.ReadAll(tr)
		h.nodes[name] = &node{mode: 0o640, content: b}
	}
}

func (h *fakeHelper) node(name string) *node {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.nodes[name]
}

// fakeTransport serves files by URL and counts requests.
type fakeTransport struct {
	mu       sync.Mutex
	files    map[string][]byte
	requests []string
}

func (f *fakeTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	u := r.URL.String()
	f.requests = append(f.requests, u)
	body, ok := f.files[u]
	status := http.StatusOK
	if !ok {
		status = http.StatusNotFound
	}
	return &http.Response{
		StatusCode: status, Status: http.StatusText(status), Request: r, Header: http.Header{},
		Body: io.NopCloser(bytes.NewReader(body)), ContentLength: int64(len(body)),
	}, nil
}

func (f *fakeTransport) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.requests)
}

type fakeResolver struct{}

func (fakeResolver) LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error) {
	return []net.IPAddr{{IP: net.ParseIP("192.0.2.10")}}, nil
}

// panelTarball is a small release as build-release.sh lays it out: no
// storage/, no .env.
func panelTarball(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, d := range []string{"bootstrap/", "bootstrap/cache/", "database/", "database/migrations/", "public/"} {
		if err := tw.WriteHeader(&tar.Header{Name: d, Typeflag: tar.TypeDir, Mode: 0o755}); err != nil {
			t.Fatal(err)
		}
	}
	for name, body := range map[string]string{"artisan": "<?php\n", "public/index.php": "<?php\n", "database/migrations/0001_init.php": "<?php\n"} {
		if err := tw.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(body))}); err != nil {
			t.Fatal(err)
		}
		tw.Write([]byte(body))
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func writeCert(t *testing.T, certPath string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	notAfter := testNow.AddDate(0, 3, 0)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: testDomain}, DNSNames: []string{testDomain}, NotBefore: testNow, NotAfter: notAfter}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	mustMkdir(t, filepath.Dir(certPath))
	mustWrite(t, certPath, string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})))
}

func mustMkdir(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
}

func mustWrite(t *testing.T, name, content string) {
	t.Helper()
	mustMkdir(t, filepath.Dir(name))
	if err := os.WriteFile(name, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func mustRead(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
