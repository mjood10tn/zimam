package sites

import (
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/user"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"zimam/agent/contract"
	"zimam/agent/internal/config"
	"zimam/agent/internal/helper"
	"zimam/agent/internal/paths"
	"zimam/agent/internal/sysexec"
)

// fakeHelper is the site-user helper with the site's home in memory, so a
// test can tell what went through it: nothing it does reaches the disk.
type fakeHelper struct {
	mu    sync.Mutex
	nodes map[string]*fakeNode // by name inside the home; "." is the home
	ops   []string             // "<account> <op> <name> [<arg>]"
}

type fakeNode struct {
	dir     bool
	mode    fs.FileMode
	target  string // a symlink's
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
		if n := h.nodes[dir]; n != nil && !n.dir {
			return fmt.Errorf("fake helper: %s is not a directory", dir)
		}
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
	fi := helper.FileInfo{Name: path.Base(name), Mode: n.mode, Size: int64(len(n.content)), Target: n.target}
	switch {
	case n.dir:
		fi.Mode |= fs.ModeDir
	case n.target != "":
		fi.Mode = fs.ModeSymlink | 0o777
	}
	return fi, nil
}

func (h *fakeHelper) SymlinkSwap(_ context.Context, a helper.Account, link, target string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.record(a, "symlink-swap", link, target)
	if n := h.nodes[path.Dir(link)]; n == nil || !n.dir {
		return fmt.Errorf("fake helper: %s has no parent directory: %w", link, fs.ErrNotExist)
	}
	h.nodes[link] = &fakeNode{target: target}
	return nil
}

// RemoveTree deletes name and everything under it; "." empties the home.
func (h *fakeHelper) RemoveTree(_ context.Context, a helper.Account, name string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.record(a, "remove-tree", name)
	for n := range h.nodes {
		if n != "." && (name == "." || n == name || strings.HasPrefix(n, name+"/")) {
			delete(h.nodes, n)
		}
	}
	return nil
}

// changes are the operations that changed the home, without the stats.
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

// hookRunner is a FakeRunner whose useradd, usermod, userdel and groupdel
// change what the account lookup and /etc/group report, as the real tools
// would, and whose pgrep finds the processes env.procs says are left.
type hookRunner struct {
	*sysexec.FakeRunner
	env *env
}

func (r hookRunner) Run(ctx context.Context, c sysexec.Cmd) (sysexec.Result, error) {
	cmd := strings.Join(append([]string{c.Path}, c.Args...), " ")
	if stderr, ok := r.env.failOnce[cmd]; ok {
		delete(r.env.failOnce, cmd)
		r.FakeRunner.Run(ctx, c) // recorded like any call
		return sysexec.Result{ExitCode: 1, Stderr: []byte(stderr)}, &sysexec.ExitError{Path: c.Path, Code: 1, Stderr: []byte(stderr)}
	}
	if c.Path == "/usr/bin/pgrep" && r.env.procs != 0 {
		r.env.procs--
		r.FakeRunner.Run(ctx, c) // recorded like any call
		return sysexec.Result{Stdout: []byte("4242\n")}, nil
	}
	res, err := r.FakeRunner.Run(ctx, c)
	if err != nil {
		return res, err
	}
	switch c.Path {
	case "/usr/sbin/useradd":
		name := c.Args[len(c.Args)-1]
		r.env.users[name] = true
		r.env.appendGroup(fmt.Sprintf("%s:x:%d:\n", name, os.Getgid()))
	case "/usr/bin/systemctl":
		// FPM makes each pool's socket once it has started or reloaded.
		if len(c.Args) >= 2 && (c.Args[0] == "reload" || c.Args[0] == "enable") && !r.env.noSockets {
			r.env.makeSockets(strings.TrimSuffix(strings.TrimPrefix(c.Args[len(c.Args)-1], "php"), "-fpm"))
		}
	case "/usr/sbin/userdel":
		delete(r.env.users, c.Args[0])
	case "/usr/sbin/groupdel":
		b, err := os.ReadFile(r.env.p.EtcGroup())
		if err != nil {
			return res, err
		}
		var kept []string
		for line := range strings.Lines(string(b)) {
			if !strings.HasPrefix(line, c.Args[0]+":") {
				kept = append(kept, line)
			}
		}
		if err := os.WriteFile(r.env.p.EtcGroup(), []byte(strings.Join(kept, "")), 0o644); err != nil {
			return res, err
		}
	case "/usr/sbin/usermod":
		group, member := c.Args[1], c.Args[2]
		b, err := os.ReadFile(r.env.p.EtcGroup())
		if err != nil {
			return res, err
		}
		var lines []string
		for line := range strings.Lines(string(b)) {
			line = strings.TrimSuffix(line, "\n")
			if strings.HasPrefix(line, group+":") {
				if strings.HasSuffix(line, ":") {
					line += member
				} else {
					line += "," + member
				}
			}
			lines = append(lines, line)
		}
		if err := os.WriteFile(r.env.p.EtcGroup(), []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
			return res, err
		}
	}
	return res, err
}

// env is a server under a temporary root, where the agent's config says
// the default PHP is 8.4 and PHP 8.4 and 8.5 are installed, Nginx and
// logrotate are set up as bootstrap leaves them, and nothing of the site
// exists yet. Every command a provision may run is scripted to succeed.
type env struct {
	t     *testing.T
	p     paths.Paths
	run   *sysexec.FakeRunner
	help  *fakeHelper
	d     sysexec.Deps
	users map[string]bool
	// failOnce makes the next run of a command, "path args...", fail with
	// this stderr.
	failOnce map[string]string
	// noSockets keeps FPM from making its pools' sockets.
	noSockets bool
	// procs is how many more times pgrep finds the site user's processes;
	// -1 is for ever.
	procs int
}

var testNow = time.Date(2026, 11, 3, 9, 0, 0, 0, time.UTC)

func newEnv(t *testing.T) *env {
	t.Helper()
	e := &env{t: t, p: paths.Paths{Root: t.TempDir()}, run: sysexec.NewFakeRunner(), help: newFakeHelper(), users: map[string]bool{}, failOnce: map[string]string{}}
	cfg := config.New("panel.example.com", "8.4", "8.4")
	if err := config.Save(e.p, cfg); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{
		e.p.LocksDir(), filepath.Dir(e.p.SiteHome("shop")), e.p.SitesLogDir(), e.p.NginxZimamDir(),
		filepath.Dir(e.p.NginxSiteAvailable("shop")), filepath.Dir(e.p.NginxSiteEnabled("shop")), e.p.LogrotateDir(),
		e.p.PHPPoolDir("8.4"), e.p.PHPPoolDir("8.5"), filepath.Dir(e.p.PHPFPMBinary("8.4")),
	} {
		mkdir(t, dir)
	}
	for _, v := range []contract.PHPVersion{"8.4", "8.5"} {
		writeFile(t, e.p.PHPFPMBinary(v), "")
	}
	writeFile(t, e.p.EtcGroup(), "root:x:0:\nwww-data:x:33:\n")
	for _, s := range []sysexec.Script{
		{Path: "/usr/sbin/useradd", Args: []string{"-M", "-d", "/home/s_shop", "-s", "/usr/sbin/nologin", "-U", "s_shop"}},
		{Path: "/usr/sbin/usermod", Args: []string{"-aG", "s_shop", "www-data"}},
		{Path: "/usr/sbin/nginx", Args: []string{"-t"}},
		{Path: "/usr/bin/systemctl", Args: []string{"reload-or-restart", "nginx"}},
		{Path: "/usr/sbin/logrotate", Args: []string{"-d", "--state", e.p.LogrotateState(), e.p.LogrotateConf()}},
		{Path: "/usr/bin/pkill", Args: []string{"-KILL", "-u", "s_shop"}, Result: sysexec.Result{ExitCode: 1}}, // nothing to kill
		{Path: "/usr/bin/pgrep", Args: []string{"-u", "s_shop"}, Result: sysexec.Result{ExitCode: 1}},          // nothing left
		{Path: "/usr/sbin/userdel", Args: []string{"s_shop"}},
		{Path: "/usr/sbin/groupdel", Args: []string{"s_shop"}},
		{Path: "/usr/bin/certbot", Args: []string{"delete", "--cert-name", "zimam-shop", "--non-interactive"}},
	} {
		e.run.Add(s)
	}
	for _, v := range []string{"8.4", "8.5"} {
		e.run.Add(sysexec.Script{Path: "/usr/sbin/php-fpm" + v, Args: []string{"-t"}})
		e.run.Add(sysexec.Script{Path: "/usr/bin/systemctl", Args: []string{"enable", "--now", "php" + v + "-fpm"}})
		e.run.Add(sysexec.Script{Path: "/usr/bin/systemctl", Args: []string{"reload", "php" + v + "-fpm"}})
		e.run.Add(sysexec.Script{Path: "/usr/bin/systemctl", Args: []string{"disable", "--now", "php" + v + "-fpm"}})
	}
	e.d = sysexec.Deps{Paths: e.p, Run: hookRunner{FakeRunner: e.run, env: e}, Config: cfg, Logf: t.Logf, Now: func() time.Time { return testNow }}
	return e
}

func (e *env) account(name string) (helper.Account, error) {
	if !e.users[name] {
		return helper.Account{}, user.UnknownUserError(name)
	}
	return helper.Account{Name: name, UID: uint32(os.Getuid()), GID: uint32(os.Getgid()), Home: "/home/" + name}, nil
}

func (e *env) appendGroup(line string) {
	f, err := os.OpenFile(e.p.EtcGroup(), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		e.t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.WriteString(line); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) provision(p contract.SiteProvisionParams) (contract.SiteProvisionResult, error) {
	return Provision(context.Background(), e.d, e.help, e.account, p)
}

// calls are the commands run so far, as "path args...".
func (e *env) calls() []string {
	var out []string
	for _, c := range e.run.Calls() {
		out = append(out, strings.Join(append([]string{c.Path}, c.Args...), " "))
	}
	return out
}

func (e *env) callsSince(n int) []string { return slices.Clone(e.calls()[n:]) }

func mkdir(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// fpmActive scripts what systemctl is-active answers for v's FPM service,
// one answer per call, the last repeating.
func (e *env) fpmActive(v string, answers ...bool) {
	for _, active := range answers {
		s := sysexec.Script{Path: "/usr/bin/systemctl", Args: []string{"is-active", "--quiet", "php" + v + "-fpm"}}
		if !active {
			s.Result.ExitCode = 3
		}
		e.run.Add(s)
	}
}

// makeSockets creates the socket of every Zimam site pool on v, as FPM does.
func (e *env) makeSockets(v string) {
	entries, err := os.ReadDir(e.p.PHPPoolDir(contract.PHPVersion(v)))
	if err != nil {
		e.t.Fatal(err)
	}
	for _, de := range entries {
		name, ok := strings.CutSuffix(strings.TrimPrefix(de.Name(), "zimam-"), ".conf")
		if !ok || !strings.HasPrefix(de.Name(), "zimam-") {
			continue
		}
		sock := e.p.PHPSocket(contract.SiteName(name), contract.PHPVersion(v))
		mkdir(e.t, filepath.Dir(sock))
		writeFile(e.t, sock, "")
	}
}
