package deploy

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"zimam/agent/contract"
	"zimam/agent/internal/config"
	"zimam/agent/internal/helper"
	"zimam/agent/internal/paths"
	"zimam/agent/internal/sites"
	"zimam/agent/internal/sysexec"
)

// fakeHelper is the site-user helper with the home in memory. Its Exec
// plays ssh-keygen: -t makes a key pair (and refuses to overwrite one, as
// the real one does with empty stdin), -y prints the private key's public
// half.
type fakeHelper struct {
	mu    sync.Mutex
	nodes map[string]*fakeNode // by name inside the home
	ops   []string             // "<account> <op> <name> [<arg>...]"
	keys  int                  // key pairs made so far
	// badPublic, when set, is what ssh-keygen -y prints.
	badPublic string
}

type fakeNode struct {
	dir     bool
	mode    fs.FileMode
	content []byte
}

func newFakeHelper() *fakeHelper {
	return &fakeHelper{nodes: map[string]*fakeNode{".": {dir: true, mode: 0o750}}}
}

func (h *fakeHelper) record(a helper.Account, op string, args ...string) {
	h.ops = append(h.ops, strings.Join(append([]string{a.Name, op}, args...), " "))
}

func (h *fakeHelper) Mkdir(_ context.Context, a helper.Account, name string, mode fs.FileMode) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.record(a, "mkdir", name, fmt.Sprintf("%o", mode))
	for dir := name; dir != "."; dir = path.Dir(dir) {
		if h.nodes[dir] == nil {
			h.nodes[dir] = &fakeNode{dir: true, mode: 0o750}
		}
	}
	h.nodes[name].mode = mode
	return nil
}

func (h *fakeHelper) Write(_ context.Context, a helper.Account, name string, mode fs.FileMode, content io.Reader) error {
	b, err := io.ReadAll(content)
	if err != nil {
		return err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.record(a, "write", name, fmt.Sprintf("%o", mode))
	if n := h.nodes[path.Dir(name)]; n == nil || !n.dir {
		return fmt.Errorf("fake helper: %s has no parent directory: %w", name, fs.ErrNotExist)
	}
	h.nodes[name] = &fakeNode{mode: mode, content: b}
	return nil
}

func (h *fakeHelper) Stat(_ context.Context, a helper.Account, name string) (helper.FileInfo, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.record(a, "stat", name)
	n := h.nodes[name]
	if n == nil {
		return helper.FileInfo{}, fmt.Errorf("fake helper: %s: %w", name, fs.ErrNotExist)
	}
	fi := helper.FileInfo{Name: path.Base(name), Mode: n.mode, Size: int64(len(n.content))}
	if n.dir {
		fi.Mode |= fs.ModeDir
	}
	return fi, nil
}

func (h *fakeHelper) RemoveTree(_ context.Context, a helper.Account, name string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.record(a, "remove-tree", name)
	for n := range h.nodes {
		if n == name || strings.HasPrefix(n, name+"/") {
			delete(h.nodes, n)
		}
	}
	return nil
}

func (h *fakeHelper) Exec(_ context.Context, a helper.Account, r helper.ExecRequest) (int, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.record(a, "exec", append([]string{r.Program, "dir=" + r.Dir}, r.Args...)...)
	if r.Program != "ssh-keygen" {
		return 1, fmt.Errorf("fake helper: unexpected program %s", r.Program)
	}
	switch {
	case slices.Contains(r.Args, "-t"):
		if h.nodes[keyFile] != nil || h.nodes[keyFile+".pub"] != nil {
			return 1, nil // "Overwrite (y/n)?" with empty stdin
		}
		if n := h.nodes[".ssh"]; n == nil || !n.dir {
			return 1, nil
		}
		h.keys++
		pub := testPublicKey(byte(h.keys))
		h.nodes[keyFile] = &fakeNode{mode: 0o600, content: []byte("PRIVATE " + pub)}
		h.nodes[keyFile+".pub"] = &fakeNode{mode: 0o644, content: []byte(pub + " zimam-shop@panel.example.com\n")}
		return 0, nil
	case slices.Contains(r.Args, "-y"):
		n := h.nodes[keyFile]
		if n == nil {
			return 1, nil
		}
		if r.Capture == nil || r.CaptureMax <= 0 {
			return 1, errors.New("fake helper: -y without capture")
		}
		out := strings.TrimPrefix(string(n.content), "PRIVATE ") + " zimam-shop@panel.example.com\n"
		if h.badPublic != "" {
			out = h.badPublic
		}
		if int64(len(out)) > r.CaptureMax {
			return 1, errors.New("fake helper: capture over its limit")
		}
		io.WriteString(r.Capture, out)
		return 0, nil
	}
	return 1, fmt.Errorf("fake helper: unexpected ssh-keygen %v", r.Args)
}

// changes are the operations that changed the home or ran a program.
func (h *fakeHelper) changes() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []string
	for _, op := range h.ops {
		if !strings.Contains(op, " stat ") {
			out = append(out, op)
		}
	}
	return out
}

func (h *fakeHelper) has(op string) bool { return slices.Contains(h.changes(), op) }

// testPublicKey is an ed25519 public key line whose key bytes are all b.
func testPublicKey(b byte) string {
	var blob []byte
	blob = binary.BigEndian.AppendUint32(blob, 11)
	blob = append(blob, "ssh-ed25519"...)
	blob = binary.BigEndian.AppendUint32(blob, 32)
	for range 32 {
		blob = append(blob, b)
	}
	return "ssh-ed25519 " + base64.StdEncoding.EncodeToString(blob)
}

func testFingerprint(pub string) string {
	blob, _ := base64.StdEncoding.DecodeString(strings.Fields(pub)[1])
	s := sha256.Sum256(blob)
	return "SHA256:" + base64.RawStdEncoding.EncodeToString(s[:])
}

const keygenArgs = "dir= -t ed25519 -N  -C zimam-shop@panel.example.com -f .ssh/zimam_deploy -q"

type env struct {
	t   *testing.T
	d   sysexec.Deps
	h   *fakeHelper
	run *sysexec.FakeRunner
}

var account = helper.Account{Name: "s_shop", UID: 1001, GID: 1001}

func newEnv(t *testing.T) *env {
	t.Helper()
	p := paths.Paths{Root: t.TempDir()}
	run := sysexec.NewFakeRunner()
	e := &env{t: t, h: newFakeHelper(), run: run, d: sysexec.Deps{
		Paths:  p,
		Run:    run,
		Config: config.New("panel.example.com", "8.4", "8.5"),
		Logf:   t.Logf,
		Now:    time.Now,
	}}
	st := sites.State{Name: "shop", Domain: "shop.example.com", Type: contract.SiteStatic, TemplateVersion: 1, Security: sites.DefaultSecurity()}
	if err := sites.Save(p, st); err != nil {
		t.Fatal(err)
	}
	e.touch(p.GitBinary())
	e.touch(p.UnzipBinary())
	return e
}

func (e *env) touch(name string) {
	e.t.Helper()
	if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		e.t.Fatal(err)
	}
	if err := os.WriteFile(name, nil, 0o755); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) lookup(username string) (helper.Account, error) {
	if username != "s_shop" {
		return helper.Account{}, fmt.Errorf("unexpected user %s", username)
	}
	a := account
	a.Home = e.d.Paths.SiteHome("shop")
	return a, nil
}

func (e *env) repo(p contract.SiteRepoParams) (contract.SiteRepoResult, error) {
	e.t.Helper()
	if p.Name == "" {
		p.Name = "shop"
	}
	return Repo(context.Background(), e.d, e.h, e.lookup, p)
}

func (e *env) mustRepo(p contract.SiteRepoParams) contract.SiteRepoResult {
	e.t.Helper()
	res, err := e.repo(p)
	if err != nil {
		e.t.Fatalf("Repo(%+v): %v", p, err)
	}
	return res
}

func (e *env) state() sites.State {
	e.t.Helper()
	st, err := sites.Load(e.d.Paths, "shop")
	if err != nil {
		e.t.Fatal(err)
	}
	return st
}

func (e *env) runCalls() []string {
	var out []string
	for _, c := range e.run.Calls() {
		out = append(out, strings.Join(append([]string{c.Path}, c.Args...), " "))
	}
	return out
}

var fingerprintShape = regexp.MustCompile(`^SHA256:[A-Za-z0-9+/]{43}$`)

func TestRepoGeneratesKeyAsSiteUser(t *testing.T) {
	e := newEnv(t)
	res := e.mustRepo(contract.SiteRepoParams{Repository: "octo/app", Branch: "main"})

	if !e.h.has("s_shop mkdir .ssh 700") {
		t.Errorf("no .ssh 0700 through the helper; changes %q", e.h.changes())
	}
	if !e.h.has("s_shop exec ssh-keygen " + keygenArgs) {
		t.Errorf("no ssh-keygen as s_shop; changes %q", e.h.changes())
	}
	if !e.h.has("s_shop exec ssh-keygen dir= -y -f .ssh/zimam_deploy") {
		t.Errorf("the public key wasn't read through ssh-keygen -y; changes %q", e.h.changes())
	}
	want := testPublicKey(1)
	if res.PublicKey != want {
		t.Errorf("public key %q, want %q", res.PublicKey, want)
	}
	if !fingerprintShape.MatchString(res.Fingerprint) || res.Fingerprint != testFingerprint(want) {
		t.Errorf("fingerprint %q, want %q", res.Fingerprint, testFingerprint(want))
	}
	if res.Name != "shop" || res.Repository != "octo/app" || res.Branch != "main" {
		t.Errorf("result %+v", res)
	}
	st := e.state()
	if st.Repository != "octo/app" || st.Branch != "main" || st.DeployKeyFingerprint != res.Fingerprint {
		t.Errorf("state %+v", st)
	}
	if calls := e.runCalls(); len(calls) != 0 {
		t.Errorf("root ran %q; git and unzip are installed", calls)
	}
}

func TestRepoKeepsExistingKey(t *testing.T) {
	e := newEnv(t)
	first := e.mustRepo(contract.SiteRepoParams{Repository: "octo/app", Branch: "main"})
	e.h.ops = nil
	second := e.mustRepo(contract.SiteRepoParams{Repository: "octo/app", Branch: "main"})
	if e.h.has("s_shop exec ssh-keygen "+keygenArgs) || e.h.has("s_shop remove-tree .ssh/zimam_deploy") {
		t.Errorf("the key was replaced; changes %q", e.h.changes())
	}
	if second.Fingerprint != first.Fingerprint || second.PublicKey != first.PublicKey {
		t.Errorf("key changed: %+v then %+v", first, second)
	}
}

func TestRepoRotatesKeyWhenAsked(t *testing.T) {
	e := newEnv(t)
	first := e.mustRepo(contract.SiteRepoParams{Repository: "octo/app", Branch: "main"})
	e.h.ops = nil
	second := e.mustRepo(contract.SiteRepoParams{Repository: "octo/app", Branch: "main", RotateKey: true})
	got := e.h.changes()
	rmKey := slices.Index(got, "s_shop remove-tree .ssh/zimam_deploy")
	rmPub := slices.Index(got, "s_shop remove-tree .ssh/zimam_deploy.pub")
	gen := slices.Index(got, "s_shop exec ssh-keygen "+keygenArgs)
	if rmKey < 0 || rmPub < 0 || gen < 0 || gen < rmKey || gen < rmPub {
		t.Fatalf("want both key files removed, then ssh-keygen (A7); changes %q", got)
	}
	if second.Fingerprint == first.Fingerprint {
		t.Errorf("fingerprint unchanged after rotation: %s", second.Fingerprint)
	}
	if st := e.state(); st.DeployKeyFingerprint != second.Fingerprint {
		t.Errorf("state keeps %s, want %s", st.DeployKeyFingerprint, second.Fingerprint)
	}
}

func TestRepoReplacesHalfAKey(t *testing.T) {
	e := newEnv(t)
	e.mustRepo(contract.SiteRepoParams{Repository: "octo/app", Branch: "main"})
	delete(e.h.nodes, keyFile+".pub") // a run killed between the two files, say
	e.h.ops = nil
	e.mustRepo(contract.SiteRepoParams{Repository: "octo/app", Branch: "main"})
	got := e.h.changes()
	if !slices.Contains(got, "s_shop remove-tree .ssh/zimam_deploy") || !slices.Contains(got, "s_shop exec ssh-keygen "+keygenArgs) {
		t.Errorf("half a key pair wasn't replaced; changes %q", got)
	}
}

func TestRepoWritesPinnedKnownHosts(t *testing.T) {
	e := newEnv(t)
	e.mustRepo(contract.SiteRepoParams{Repository: "octo/app", Branch: "main"})
	if !e.h.has("s_shop write .ssh/known_hosts 600") {
		t.Fatalf("known_hosts not written 0600; changes %q", e.h.changes())
	}
	if got := string(e.h.nodes[".ssh/known_hosts"].content); got != GitHubKnownHosts {
		t.Errorf("known_hosts = %q, want the compiled keys", got)
	}
	// A later run writes the pins again, so a Zimam release that moves them
	// reaches every site on its next site.repo.
	e.h.nodes[".ssh/known_hosts"].content = []byte("github.com ssh-ed25519 AAAAplanted\n")
	e.mustRepo(contract.SiteRepoParams{Repository: "octo/app", Branch: "main"})
	if got := string(e.h.nodes[".ssh/known_hosts"].content); got != GitHubKnownHosts {
		t.Errorf("known_hosts = %q after a second run, want the compiled keys", got)
	}
}

// TestGitHubHostKeysMatchPins checks every compiled key against the
// fingerprints of spike amendment A5, which GitHub's documentation and
// api.github.com/meta agreed on (2026-11-15).
func TestGitHubHostKeysMatchPins(t *testing.T) {
	want := map[string]string{
		"ssh-ed25519":         "SHA256:+DiY3wvvV6TuJJhbpZisF/zLDA0zPMSvHdkr4UvCOqU",
		"ecdsa-sha2-nistp256": "SHA256:p2QAMXNIC1TJYWeIOttrVc98/R1BUFWu3/LiyKgUfQM",
		"ssh-rsa":             "SHA256:uNiVztksCsDhcc0u9e8BujQXVUpKZIDTMczCvj3tD2s",
	}
	lines := strings.Split(strings.TrimSuffix(GitHubKnownHosts, "\n"), "\n")
	if len(lines) != len(want) {
		t.Fatalf("%d host keys, want %d", len(lines), len(want))
	}
	for _, line := range lines {
		f := strings.Fields(line)
		if len(f) != 3 || f[0] != "github.com" {
			t.Fatalf("line %q is not `github.com <type> <key>`", line)
		}
		blob, err := base64.StdEncoding.DecodeString(f[2])
		if err != nil {
			t.Fatalf("%s: %v", f[1], err)
		}
		if got := fingerprint(blob); got != want[f[1]] {
			t.Errorf("%s fingerprint %s, want %s", f[1], got, want[f[1]])
		}
		delete(want, f[1])
	}
	if len(want) != 0 {
		t.Errorf("missing keys: %v", want)
	}
}

func TestGitSSHCommand(t *testing.T) {
	// R02-D3 with A5's GlobalKnownHostsFile, so only the pinned file decides.
	const want = "ssh -i ~/.ssh/zimam_deploy -o IdentitiesOnly=yes -o StrictHostKeyChecking=yes " +
		"-o UserKnownHostsFile=~/.ssh/known_hosts -o GlobalKnownHostsFile=/dev/null -o BatchMode=yes"
	if GitSSHCommand != want {
		t.Errorf("GitSSHCommand = %q, want %q", GitSSHCommand, want)
	}
	if got := RemoteURL("octo/app"); got != "git@github.com:octo/app.git" {
		t.Errorf("RemoteURL = %q", got)
	}
}

func TestRepoChangeDropsMirror(t *testing.T) {
	e := newEnv(t)
	e.mustRepo(contract.SiteRepoParams{Repository: "octo/app", Branch: "main"})
	mirror := func() { e.h.nodes["site/repo.git"] = &fakeNode{dir: true, mode: 0o700} }
	for _, tc := range []struct {
		repo   contract.Repository
		branch contract.Branch
		drop   bool
	}{
		{"octo/app", "main", false},
		{"octo/app", "release", true},
		{"octo/other", "release", true},
		{"octo/other", "release", false},
	} {
		mirror()
		e.h.ops = nil
		e.mustRepo(contract.SiteRepoParams{Repository: tc.repo, Branch: tc.branch})
		got := e.h.changes()
		drop := slices.Index(got, "s_shop remove-tree site/repo.git")
		if (drop >= 0) != tc.drop {
			t.Errorf("%s %s: dropped = %v, want %v; changes %q", tc.repo, tc.branch, drop >= 0, tc.drop, got)
		}
		if st := e.state(); st.Repository != tc.repo || st.Branch != tc.branch {
			t.Errorf("%s %s: state %+v", tc.repo, tc.branch, st)
		}
	}
}

func TestRepoInstallsGitWhenMissing(t *testing.T) {
	update := sysexec.Script{Path: "/usr/bin/apt-get", Args: []string{"-o", "DPkg::Lock::Timeout=300", "update"}}
	install := func(pkgs ...string) sysexec.Script {
		return sysexec.Script{Path: "/usr/bin/apt-get", Args: append([]string{"-o", "DPkg::Lock::Timeout=300",
			"-o", "Dpkg::Options::=--force-confdef", "-o", "Dpkg::Options::=--force-confold",
			"install", "-y", "--no-install-recommends"}, pkgs...)}
	}
	for _, tc := range []struct {
		name    string
		missing []string
	}{
		{"git", []string{"git"}},
		{"unzip", []string{"unzip"}},
		{"both", []string{"git", "unzip"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			bin := map[string]string{"git": e.d.Paths.GitBinary(), "unzip": e.d.Paths.UnzipBinary()}
			for _, m := range tc.missing {
				if err := os.Remove(bin[m]); err != nil {
					t.Fatal(err)
				}
			}
			e.run.Add(update)
			e.run.Add(install(tc.missing...))
			e.mustRepo(contract.SiteRepoParams{Repository: "octo/app", Branch: "main"})
			want := []string{
				"/usr/bin/apt-get -o DPkg::Lock::Timeout=300 update",
				strings.Join(append([]string{install().Path}, install(tc.missing...).Args...), " "),
			}
			if got := e.runCalls(); !slices.Equal(got, want) {
				t.Errorf("calls %q, want %q", got, want)
			}
			for _, c := range e.run.Calls() {
				if !slices.Contains(c.Env, "DEBIAN_FRONTEND=noninteractive") {
					t.Errorf("%v ran without DEBIAN_FRONTEND=noninteractive", c.Args)
				}
			}
		})
	}
}

func TestRepoStopsWhenInstallFails(t *testing.T) {
	e := newEnv(t)
	os.Remove(e.d.Paths.GitBinary())
	e.run.Add(sysexec.Script{Path: "/usr/bin/apt-get", Args: []string{"-o", "DPkg::Lock::Timeout=300", "update"}})
	e.run.Add(sysexec.Script{Path: "/usr/bin/apt-get", Args: []string{"-o", "DPkg::Lock::Timeout=300",
		"-o", "Dpkg::Options::=--force-confdef", "-o", "Dpkg::Options::=--force-confold",
		"install", "-y", "--no-install-recommends", "git"}, Result: sysexec.Result{ExitCode: 100}})
	if _, err := e.repo(contract.SiteRepoParams{Repository: "octo/app", Branch: "main"}); err == nil {
		t.Fatal("Repo succeeded without git")
	}
	if len(e.h.changes()) != 0 {
		t.Errorf("the home changed: %q", e.h.changes())
	}
	if st := e.state(); st.Repository != "" {
		t.Errorf("state saved: %+v", st)
	}
}

func TestRepoRefusesBadPublicKey(t *testing.T) {
	for _, bad := range []string{
		"\n",
		"no newline",
		"ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAABgQC\n",
		"ssh-ed25519 !!!!\n",
		"ssh-ed25519 " + base64.StdEncoding.EncodeToString([]byte("too short")) + "\n",
		testPublicKey(1) + "\n" + testPublicKey(2) + "\n",
		// The blob says ssh-rsa under an ssh-ed25519 label.
		"ssh-ed25519 " + base64.StdEncoding.EncodeToString(append(binary.BigEndian.AppendUint32(nil, 7), []byte("ssh-rsa")...)) + "\n",
	} {
		e := newEnv(t)
		e.h.badPublic = bad
		if res, err := e.repo(contract.SiteRepoParams{Repository: "octo/app", Branch: "main"}); err == nil {
			t.Errorf("ssh-keygen -y printing %q: accepted %+v", bad, res)
		}
		if st := e.state(); st.Repository != "" || st.DeployKeyFingerprint != "" {
			t.Errorf("ssh-keygen -y printing %q: state saved %+v", bad, st)
		}
	}
}

func TestParsePublicKeyDropsComment(t *testing.T) {
	key, fp, err := parsePublicKey([]byte(testPublicKey(7) + " anything the site user wrote\n"))
	if err != nil || key != testPublicKey(7) || fp != testFingerprint(testPublicKey(7)) {
		t.Errorf("parsePublicKey = %q, %q, %v", key, fp, err)
	}
}

func TestCheckRepoUnknownSite(t *testing.T) {
	e := newEnv(t)
	err := CheckRepo(e.d, contract.SiteRepoParams{Name: "blog", Repository: "octo/app", Branch: "main"})
	var ae *contract.AgentError
	if !errors.As(err, &ae) || ae.Code != contract.ErrNotFound {
		t.Fatalf("err = %v, want not_found", err)
	}
	if _, err := e.repo(contract.SiteRepoParams{Name: "blog", Repository: "octo/app", Branch: "main"}); !errors.As(err, &ae) || ae.Code != contract.ErrNotFound {
		t.Fatalf("Repo: err = %v, want not_found", err)
	}
	if err := CheckRepo(e.d, contract.SiteRepoParams{Name: "shop", Repository: "octo/app", Branch: "main"}); err != nil {
		t.Errorf("CheckRepo(shop) = %v", err)
	}
}

// FuzzParsePublicKey feeds the parser what a site user could put in its
// own key files: whatever it accepts is one ed25519 key with a well-formed
// fingerprint that matches it.
func FuzzParsePublicKey(f *testing.F) {
	f.Add([]byte(testPublicKey(1) + " zimam-shop@panel.example.com\n"))
	f.Add([]byte(testPublicKey(2) + "\n"))
	f.Add([]byte("ssh-rsa AAAA\n"))
	f.Add([]byte("ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMqqnkVzrm0SdG6UOoqKLsabgH5C9okWi0dh2l9GKJl\r\n"))
	f.Fuzz(func(t *testing.T, out []byte) {
		key, fp, err := parsePublicKey(out)
		if err != nil {
			return
		}
		if !regexp.MustCompile(`^ssh-ed25519 [A-Za-z0-9+/]{68}$`).MatchString(key) {
			t.Fatalf("accepted key %q", key)
		}
		if !fingerprintShape.MatchString(fp) || fp != testFingerprint(key) {
			t.Fatalf("fingerprint %q for %q", fp, key)
		}
	})
}
