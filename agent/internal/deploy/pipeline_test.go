package deploy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
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
	"zimam/agent/internal/sites"
	"zimam/agent/internal/sysexec"
)

// localHelper is the site-user helper run in this process, as the current
// user: its file operations are the real ones (helper.Serve inside an
// os.Root of the home), and Exec runs the real program (git, bash) with the
// helper's program table, so the pipeline meets real git archive output and
// the real extract-tar. Only the user switch and the process group are
// missing; pipeline_root_test.go covers those. A hook may answer a program
// instead, as for Composer, which needs a PHP this machine may lack.
type localHelper struct {
	t        *testing.T
	upstream string // where git@github.com:octo/app.git really is

	mu   sync.Mutex
	ops  []string             // "<op> <args...>", file operations and execs in order
	reqs []helper.ExecRequest // every Exec, in order
	// hook answers an Exec instead of running it, when handled.
	hook func(a helper.Account, r helper.ExecRequest) (code int, handled bool)
	// failRemove makes RemoveTree of a name fail.
	failRemove map[string]error
	// failSwap makes SymlinkSwap of a link fail; swapThenFail makes it swap,
	// then report an error, as a cancel or a failed directory sync can.
	failSwap, swapThenFail map[string]error
	// failExec makes an Exec fail without a result, by "<program> <first
	// argument>", as a helper that died would.
	failExec map[string]error
}

func (h *localHelper) record(op string, args ...string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.ops = append(h.ops, strings.Join(append([]string{op}, args...), " "))
}

// serve runs one helper operation in a's home and decodes its JSON line.
func (h *localHelper) serve(ctx context.Context, a helper.Account, op string, stdin io.Reader, out any, args ...string) error {
	h.record(op, args...)
	// The real client fails at once on a cancelled context.
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("as-user %s: %w", op, err)
	}
	root, err := os.OpenRoot(a.Home)
	if err != nil {
		return err
	}
	defer root.Close()
	if stdin == nil {
		stdin = strings.NewReader("")
	}
	var stdout, stderr bytes.Buffer
	switch code := helper.Serve(root, op, args, stdin, &stdout, &stderr); code {
	case 0:
		if out == nil {
			return nil
		}
		return json.Unmarshal(stdout.Bytes(), out)
	case 3:
		return fmt.Errorf("as-user %s: %s: %w", op, bytes.TrimSpace(stderr.Bytes()), fs.ErrNotExist)
	default:
		return fmt.Errorf("as-user %s: %s", op, bytes.TrimSpace(stderr.Bytes()))
	}
}

func octal(m fs.FileMode) string { return fmt.Sprintf("%o", m) }

func (h *localHelper) Mkdir(ctx context.Context, a helper.Account, name string, mode fs.FileMode) error {
	return h.serve(ctx, a, "mkdir", nil, nil, name, octal(mode))
}

func (h *localHelper) Write(ctx context.Context, a helper.Account, name string, mode fs.FileMode, content io.Reader) error {
	return h.serve(ctx, a, "write", content, nil, name, octal(mode))
}

func (h *localHelper) Stat(ctx context.Context, a helper.Account, name string) (helper.FileInfo, error) {
	var fi helper.FileInfo
	err := h.serve(ctx, a, "stat", nil, &fi, name)
	return fi, err
}

func (h *localHelper) List(ctx context.Context, a helper.Account, dir string) (helper.Listing, error) {
	var l helper.Listing
	err := h.serve(ctx, a, "list", nil, &l, dir)
	return l, err
}

func (h *localHelper) SymlinkSwap(ctx context.Context, a helper.Account, link, target string) error {
	h.mu.Lock()
	fail, after := h.failSwap[link], h.swapThenFail[link]
	h.mu.Unlock()
	if fail != nil {
		h.record("symlink-swap", link, target, "(fails)")
		return fail
	}
	if err := h.serve(ctx, a, "symlink-swap", nil, nil, link, target); err != nil {
		return err
	}
	return after
}

func (h *localHelper) RemoveTree(ctx context.Context, a helper.Account, name string) error {
	h.mu.Lock()
	err := h.failRemove[name]
	h.mu.Unlock()
	if err != nil {
		h.record("remove-tree", name, "(fails)")
		return err
	}
	return h.serve(ctx, a, "remove-tree", nil, nil, name)
}

func (h *localHelper) Chmod(ctx context.Context, a helper.Account, name string, mode fs.FileMode) error {
	return h.serve(ctx, a, "chmod", nil, nil, name, octal(mode))
}

func (h *localHelper) ExtractTar(ctx context.Context, a helper.Account, dest string, tarball io.Reader, lim helper.ExtractLimits) error {
	// The real helper reads a pipe: whatever stopped the client's stdin, it
	// sees only an early end.
	return h.serve(ctx, a, "extract-tar", eofOnError{tarball}, nil, dest, fmt.Sprint(lim.MaxBytes), fmt.Sprint(lim.MaxEntries))
}

func (h *localHelper) Exec(ctx context.Context, a helper.Account, r helper.ExecRequest) (int, error) {
	if a.Name != "s_shop" {
		h.t.Errorf("exec %s as %s, want the site user s_shop", r.Program, a.Name)
	}
	h.record("exec", append([]string{r.Program, "dir=" + r.Dir}, r.Args...)...)
	var stdin []byte
	if r.Stdin != nil {
		var err error
		if stdin, err = io.ReadAll(r.Stdin); err != nil {
			return 0, err
		}
		r.Stdin = bytes.NewReader(stdin)
	}
	h.mu.Lock()
	saved := r
	if r.Stdin != nil {
		saved.Stdin = bytes.NewReader(stdin)
	}
	h.reqs = append(h.reqs, saved)
	hook := h.hook
	var failed error
	if len(r.Args) > 0 {
		failed = h.failExec[r.Program+" "+r.Args[0]]
	}
	h.mu.Unlock()
	if failed != nil {
		return 0, failed
	}
	if hook != nil {
		if code, ok := hook(a, r); ok {
			// As the real client: a cancel wins over the program's exit.
			if err := ctx.Err(); err != nil {
				return 0, err
			}
			return code, nil
		}
	}
	path, lead, err := helper.ProgramArgv(r.Program, r.PHP)
	if err != nil {
		return 0, err
	}
	if r.Dir != "" && !filepath.IsLocal(r.Dir) {
		return 0, fmt.Errorf("exec %s: dir %q leaves the home", r.Program, r.Dir)
	}
	cmd := exec.CommandContext(ctx, path, append(lead, r.Args...)...)
	cmd.Dir = filepath.Join(a.Home, r.Dir)
	cmd.Env = append(hermeticGit(a.Home),
		"GIT_CONFIG_COUNT=1", "GIT_CONFIG_KEY_0=url."+h.upstream+".insteadOf", "GIT_CONFIG_VALUE_0="+RemoteURL("octo/app"))
	for k, v := range r.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	cmd.Stdin = r.Stdin
	var logged bytes.Buffer
	cmd.Stderr = &logged
	if r.Capture != nil {
		cmd.Stdout = &cappedWriter{w: r.Capture, left: r.CaptureMax}
	} else {
		cmd.Stdout = &logged
	}
	err = cmd.Run()
	if r.Log != nil {
		for line := range strings.Lines(logged.String()) {
			r.Log(strings.TrimSuffix(line, "\n"))
		}
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() >= 0 {
		return exit.ExitCode(), nil
	}
	return 0, err
}

// cappedWriter fails past its limit, as the helper's capture does.
type cappedWriter struct {
	w    io.Writer
	left int64
}

func (c *cappedWriter) Write(p []byte) (int, error) {
	if int64(len(p)) > c.left {
		return 0, errors.New("the captured output passed its limit")
	}
	c.left -= int64(len(p))
	return c.w.Write(p)
}

// hermeticGit is an environment in which git reads no system or user
// config, so the owner's own settings stay out of the tests.
func hermeticGit(home string) []string {
	return []string{
		"PATH=/usr/bin:/bin", "HOME=" + home, "LANG=C.UTF-8",
		"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.com", "GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.com",
	}
}

// upstreamRepo is the repository a test deploys from, standing in for
// GitHub, on the branch main.
type upstreamRepo struct {
	t   *testing.T
	dir string
}

func newUpstream(t *testing.T) *upstreamRepo {
	t.Helper()
	if _, err := os.Stat("/usr/bin/git"); err != nil {
		t.Skip("needs /usr/bin/git")
	}
	u := &upstreamRepo{t: t, dir: t.TempDir()}
	u.git("init", "-q", "-b", "main")
	return u
}

func (u *upstreamRepo) git(args ...string) string {
	u.t.Helper()
	cmd := exec.Command("/usr/bin/git", args...)
	cmd.Dir, cmd.Env = u.dir, hermeticGit(u.dir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		u.t.Fatalf("git %q: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// commit writes files (a value starting with "-> " is a symlink to the
// rest) and commits them; it returns the commit.
func (u *upstreamRepo) commit(files map[string]string) contract.CommitSHA {
	u.t.Helper()
	for name, content := range files {
		p := filepath.Join(u.dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			u.t.Fatal(err)
		}
		os.Remove(p)
		var err error
		if target, ok := strings.CutPrefix(content, "-> "); ok {
			err = os.Symlink(target, p)
		} else {
			err = os.WriteFile(p, []byte(content), 0o644)
		}
		if err != nil {
			u.t.Fatal(err)
		}
	}
	u.git("add", "-A")
	u.git("commit", "-q", "--allow-empty", "-m", "change")
	return contract.CommitSHA(u.git("rev-parse", "HEAD"))
}

const (
	testRepoFP = "SHA256:+DiY3wvvV6TuJJhbpZisF/zLDA0zPMSvHdkr4UvCOqU"
	initialRel = "releases/initial"
)

var startTime = time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)

// deployEnv is a provisioned site with a repository, under a temporary
// root: its home holds site/releases/initial and site/current pointing at
// it, as site.provision leaves them.
type deployEnv struct {
	t        *testing.T
	p        paths.Paths
	d        sysexec.Deps
	a        helper.Account
	h        *localHelper
	up       *upstreamRepo
	now      time.Time
	logs     []string
	composer int // composer.Ensure calls
}

func newDeployEnv(t *testing.T, typ contract.SiteType) *deployEnv {
	t.Helper()
	e := &deployEnv{t: t, p: paths.Paths{Root: t.TempDir()}, up: newUpstream(t), now: startTime}
	home := e.p.SiteHome("shop")
	for _, dir := range []string{"site/releases/initial/public", "logs", "tmp"} {
		if err := os.MkdirAll(filepath.Join(home, dir), 0o750); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(home, "site/releases/initial/public/index.html"), []byte("placeholder"), 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(initialRel, filepath.Join(home, "site/current")); err != nil {
		t.Fatal(err)
	}
	st := sites.State{Name: "shop", Domain: "shop.example.com", Type: typ, TemplateVersion: 1, Security: sites.DefaultSecurity(),
		Repository: "octo/app", Branch: "main", DeployKeyFingerprint: testRepoFP}
	if typ.RunsPHP() {
		st.PHPVersion = "8.4"
	}
	if err := sites.Save(e.p, st); err != nil {
		t.Fatal(err)
	}
	e.a = helper.Account{Name: "s_shop", UID: uint32(os.Getuid()), GID: uint32(os.Getgid()), Home: home}
	e.h = &localHelper{t: t, upstream: e.up.dir}
	// Composer is answered: it needs a PHP this machine may not have.
	e.h.hook = func(a helper.Account, r helper.ExecRequest) (int, bool) {
		if r.Program != "composer" {
			return 0, false
		}
		os.MkdirAll(filepath.Join(a.Home, r.Dir, "vendor"), 0o750)
		os.WriteFile(filepath.Join(a.Home, r.Dir, "vendor/autoload.php"), []byte("<?php"), 0o640)
		return 0, true
	}
	e.d = sysexec.Deps{
		Paths:  e.p,
		Run:    sysexec.NewFakeRunner(),
		Config: config.New("panel.example.com", "8.4", "8.4"),
		Logf: func(format string, args ...any) {
			line := fmt.Sprintf(format, args...)
			e.logs = append(e.logs, line)
			t.Log(line)
		},
		Now: func() time.Time { return e.now },
	}
	old := ensureComposer
	ensureComposer = func(context.Context, sysexec.Deps) (string, error) {
		e.composer++
		return "/usr/local/lib/zimam/composer/composer-test.phar", nil
	}
	t.Cleanup(func() { ensureComposer = old })
	return e
}

func (e *deployEnv) lookup(name string) (helper.Account, error) {
	if name != e.a.Name {
		return helper.Account{}, fmt.Errorf("unexpected account %s", name)
	}
	return e.a, nil
}

func (e *deployEnv) deploy(p contract.SiteDeployParams) (contract.SiteDeployResult, error) {
	e.t.Helper()
	if p.Name == "" {
		p.Name = "shop"
	}
	if p.Trigger == "" {
		p.Trigger = contract.DeployManual
	}
	return Run(context.Background(), e.d, e.h, e.lookup, p)
}

// mustDeploy deploys and moves the clock a minute on.
func (e *deployEnv) mustDeploy(p contract.SiteDeployParams) contract.SiteDeployResult {
	e.t.Helper()
	res, err := e.deploy(p)
	if err != nil {
		e.t.Fatalf("deploy: %v", err)
	}
	e.now = e.now.Add(time.Minute)
	return res
}

func (e *deployEnv) home(rel string) string { return filepath.Join(e.a.Home, rel) }

func (e *deployEnv) current() string {
	e.t.Helper()
	target, err := os.Readlink(e.home("site/current"))
	if err != nil {
		e.t.Fatal(err)
	}
	return target
}

func (e *deployEnv) releases() []string {
	e.t.Helper()
	entries, err := os.ReadDir(e.home("site/releases"))
	if err != nil {
		e.t.Fatal(err)
	}
	var names []string
	for _, en := range entries {
		names = append(names, en.Name())
	}
	return names
}

func (e *deployEnv) state() sites.State {
	e.t.Helper()
	st, err := sites.Load(e.p, "shop")
	if err != nil {
		e.t.Fatal(err)
	}
	return st
}

// opsFrom returns the operations recorded from index i on, without the
// reads (stat, list), which only look.
func (e *deployEnv) opsFrom(i int) []string {
	var ops []string
	for _, op := range e.h.ops[i:] {
		if !strings.HasPrefix(op, "stat ") && !strings.HasPrefix(op, "list ") {
			ops = append(ops, op)
		}
	}
	return ops
}

func (e *deployEnv) mkShared(withEnv, withStorage bool) {
	e.t.Helper()
	if withStorage {
		if err := os.MkdirAll(e.home("site/shared/storage/logs"), 0o750); err != nil {
			e.t.Fatal(err)
		}
	}
	if withEnv {
		os.MkdirAll(e.home("site/shared"), 0o750)
		if err := os.WriteFile(e.home("site/shared/.env"), []byte("APP_KEY=x\n"), 0o600); err != nil {
			e.t.Fatal(err)
		}
	}
}

func wantNoErr(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func checkModeOf(t *testing.T, p string, want fs.FileMode) {
	t.Helper()
	fi, err := os.Lstat(p)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode() != want {
		t.Errorf("%s mode %v, want %v", p, fi.Mode(), want)
	}
}

func TestReleaseIDFromClockAndCommit(t *testing.T) {
	riyadh := time.FixedZone("AST", 3*60*60)
	at := time.Date(2026, 11, 15, 2, 4, 5, 999, riyadh) // 23:04:05 UTC the day before
	got := releaseID(at, "abcdef0123456789abcdef0123456789abcdef01")
	if got != "20261114230405-abcdef0" {
		t.Errorf("releaseID = %q, want 20261114230405-abcdef0", got)
	}
	if err := got.Validate(); err != nil {
		t.Errorf("releaseID %q: %v", got, err)
	}
}

// TestDeployStepsInOrder runs R02-D5 on a PHP site with a shared .env and
// storage/: fetch, archive into extract-tar, links, Composer, script,
// permissions, swap, mark (A3: no reload), prune.
func TestDeployStepsInOrder(t *testing.T) {
	e := newDeployEnv(t, contract.SitePHP)
	e.mkShared(true, true)
	sha := e.up.commit(map[string]string{
		"composer.json":              "{}",
		"public/index.php":           "<?php echo 1;",
		"bootstrap/cache/.gitignore": "*\n!.gitignore\n",
		"storage/logs/.gitignore":    "*\n!.gitignore\n", // the repository's own storage/ gives way to the shared one
		".env":                       "APP_KEY=from-the-repo\n",
	})
	res := e.mustDeploy(contract.SiteDeployParams{Composer: true, Script: "echo built > built.txt\n"})

	id := "20261115120000-" + string(sha[:7])
	rel := "site/releases/" + id
	if res.Name != "shop" || string(res.Release) != id || res.Commit != sha || res.PreviousRelease != nil {
		t.Errorf("result %+v; want release %s, commit %s and no previous release", res, id, sha)
	}
	want := []string{
		"exec git dir= init --quiet --bare site/repo.git",
		"exec git dir=site/repo.git remote add origin git@github.com:octo/app.git",
		"exec git dir=site/repo.git fetch --prune --no-tags --refmap= origin +refs/heads/main:refs/heads/main",
		"exec git dir=site/repo.git rev-parse --verify --end-of-options refs/heads/main^{commit}",
		"exec git dir=site/repo.git archive --format=tar.gz --end-of-options " + string(sha) + "^{tree}",
		"extract-tar " + rel + " 1073741824 200000",
		"remove-tree " + rel + "/.env",
		"symlink-swap " + rel + "/.env ../../shared/.env",
		"remove-tree " + rel + "/storage",
		"symlink-swap " + rel + "/storage ../../shared/storage",
		"exec composer dir=" + rel + " install --no-dev --no-interaction --prefer-dist --optimize-autoloader --no-progress",
		"exec deploy-script dir=" + rel,
		"chmod " + rel + "/bootstrap/cache 700",
		"chmod " + rel + "/bootstrap/cache/.gitignore 600",
		"symlink-swap site/current releases/" + id,
	}
	// The archive and extract-tar run together; the order between them is
	// the scheduler's.
	got := e.opsFrom(0)
	if len(got) > 5 && strings.HasPrefix(got[4], "extract-tar") {
		got[4], got[5] = got[5], got[4]
	}
	if !slices.Equal(got, want) {
		t.Errorf("operations\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	// The log names each step, in order.
	steps := []string{"Fetching", "Exporting", "Linking", "Running Composer", "Running the deploy script", "Tightening", "Making", "Marking", "Pruning"}
	i := 0
	for _, line := range e.logs {
		if i < len(steps) && strings.HasPrefix(line, steps[i]) {
			i++
		}
	}
	if i != len(steps) {
		t.Errorf("the log names the steps up to %q, not %q:\n%s", steps[:i], steps[i], strings.Join(e.logs, "\n"))
	}
	for _, r := range e.h.reqs {
		if r.Program == "git" && r.Env["GIT_SSH_COMMAND"] != GitSSHCommand {
			t.Errorf("git %q ran with GIT_SSH_COMMAND %q", r.Args, r.Env["GIT_SSH_COMMAND"])
		}
	}
	if e.current() != "releases/"+id {
		t.Errorf("current -> %s", e.current())
	}
	if got := readFile(t, e.home("site/current/public/index.php")); got != "<?php echo 1;" {
		t.Errorf("index.php = %q", got)
	}
	if got := readFile(t, e.home("site/current/built.txt")); got != "built\n" {
		t.Errorf("the script's file = %q", got)
	}
	if got := readFile(t, e.home("site/current/.env")); got != "APP_KEY=x\n" {
		t.Errorf("the release's .env = %q, want the shared one", got)
	}
	if target, _ := os.Readlink(e.home(rel + "/storage")); target != "../../shared/storage" {
		t.Errorf("storage -> %q", target)
	}
	if _, err := os.Stat(e.home(rel + "/vendor/autoload.php")); err != nil {
		t.Errorf("Composer's vendor/: %v", err)
	}
	if e.composer != 1 {
		t.Errorf("composer.Ensure ran %d times, want 1", e.composer)
	}
	st := e.state()
	wantRel := []contract.Release{{ID: contract.ReleaseID(id), Commit: sha, Created: startTime, Active: true}}
	if !slices.Equal(st.Releases, wantRel) {
		t.Errorf("state releases %+v, want %+v", st.Releases, wantRel)
	}
	// The first deploy's previous release is the placeholder, which stays.
	if !slices.Equal(e.releases(), []string{id, "initial"}) {
		t.Errorf("releases %q", e.releases())
	}
}

// TestDeployComposerEnvironment: Composer runs as the site user with the
// site's PHP version, its home and cache in the site's home (R02-D6).
func TestDeployComposerEnvironment(t *testing.T) {
	e := newDeployEnv(t, contract.SitePHP)
	e.up.commit(map[string]string{"composer.json": "{}"})
	e.mustDeploy(contract.SiteDeployParams{Composer: true})
	var found bool
	for _, r := range e.h.reqs {
		if r.Program != "composer" {
			continue
		}
		found = true
		want := map[string]string{"COMPOSER_HOME": e.a.Home + "/.composer", "COMPOSER_CACHE_DIR": e.a.Home + "/.cache/composer", "COMPOSER_NO_INTERACTION": "1"}
		if r.PHP != "8.4" || fmt.Sprint(r.Env) != fmt.Sprint(want) || r.Stdin != nil {
			t.Errorf("composer ran with PHP %q, env %v, stdin %v; want 8.4 and %v", r.PHP, r.Env, r.Stdin, want)
		}
	}
	if !found {
		t.Error("Composer didn't run")
	}
}

// TestFirstDeployInitsBareRepo (spike amendment A2): the first deploy
// makes an empty bare repository with GitHub as origin, not a mirror clone;
// later ones only fetch.
func TestFirstDeployInitsBareRepo(t *testing.T) {
	e := newDeployEnv(t, contract.SiteStatic)
	e.up.commit(map[string]string{"index.html": "one"})
	e.up.git("tag", "v1")
	e.up.git("branch", "other")
	e.mustDeploy(contract.SiteDeployParams{})
	if fi, err := os.Stat(e.home("site/repo.git/HEAD")); err != nil || !fi.Mode().IsRegular() {
		t.Fatalf("site/repo.git is no bare repository: %v", err)
	}
	refs := gitIn(t, e.home("site/repo.git"), "for-each-ref", "--format=%(refname)")
	if refs != "refs/heads/main" {
		t.Errorf("the site's repository holds %q; want only refs/heads/main", refs)
	}
	if url := gitIn(t, e.home("site/repo.git"), "config", "remote.origin.url"); url != "git@github.com:octo/app.git" {
		t.Errorf("origin is %q", url)
	}
	n := len(e.h.ops)
	e.up.commit(map[string]string{"index.html": "two"})
	e.mustDeploy(contract.SiteDeployParams{})
	for _, op := range e.opsFrom(n) {
		if strings.Contains(op, " init ") || strings.Contains(op, " remote ") {
			t.Errorf("the second deploy ran %q", op)
		}
	}
	if got := readFile(t, e.home("site/current/index.html")); got != "two" {
		t.Errorf("index.html = %q, want two", got)
	}
}

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("/usr/bin/git", args...)
	cmd.Dir, cmd.Env = dir, hermeticGit(dir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %q: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestFirstDeployInitFailureLeavesNoRepo: a first deploy that fails between
// init and remote add drops the half-made repository, so the next one
// starts again rather than fetching from no origin.
func TestFirstDeployInitFailureLeavesNoRepo(t *testing.T) {
	e := newDeployEnv(t, contract.SiteStatic)
	e.up.commit(map[string]string{"index.html": "one"})
	hook := e.h.hook
	e.h.hook = func(a helper.Account, r helper.ExecRequest) (int, bool) {
		if r.Program == "git" && len(r.Args) > 0 && r.Args[0] == "remote" {
			return 1, true
		}
		return hook(a, r)
	}
	if _, err := e.deploy(contract.SiteDeployParams{}); err == nil {
		t.Fatal("the deploy passed with git remote add failing")
	}
	if _, err := os.Lstat(e.home("site/repo.git")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("site/repo.git is left: %v", err)
	}
	e.h.hook = hook
	e.mustDeploy(contract.SiteDeployParams{})
}

func TestBranchNotFound(t *testing.T) {
	e := newDeployEnv(t, contract.SiteStatic)
	e.up.commit(map[string]string{"index.html": "one"})
	st := e.state()
	st.Branch = "release"
	wantNoErr(t, sites.Save(e.p, st))
	_, err := e.deploy(contract.SiteDeployParams{})
	var ae *contract.AgentError
	if !errors.As(err, &ae) || ae.Code != contract.ErrNotFound || !strings.Contains(ae.Message, "release") {
		t.Fatalf("deploy of a missing branch = %v; want not_found naming the branch", err)
	}
	if !slices.Equal(e.releases(), []string{"initial"}) || e.current() != initialRel {
		t.Errorf("releases %q, current %s", e.releases(), e.current())
	}
}

// TestGivenCommitMustBeOnBranch: a given commit deploys when the branch
// reaches it, and is refused, naming it, when it doesn't.
func TestGivenCommitMustBeOnBranch(t *testing.T) {
	e := newDeployEnv(t, contract.SiteStatic)
	old := e.up.commit(map[string]string{"index.html": "old"})
	e.up.commit(map[string]string{"index.html": "new"})
	e.up.git("checkout", "-q", "-b", "feature")
	feature := e.up.commit(map[string]string{"index.html": "feature"})
	e.up.git("checkout", "-q", "main")

	for _, c := range []contract.CommitSHA{feature, "0123456789abcdef0123456789abcdef01234567"} {
		_, err := e.deploy(contract.SiteDeployParams{Commit: &c})
		var ae *contract.AgentError
		if !errors.As(err, &ae) || ae.Code != contract.ErrValidationFailed || !strings.Contains(ae.Error(), string(c)) || !strings.Contains(ae.Error(), "main") {
			t.Errorf("deploy of %s = %v; want validation_failed naming the commit and the branch", c, err)
		}
	}
	if !slices.Equal(e.releases(), []string{"initial"}) {
		t.Errorf("refused deploys left releases %q", e.releases())
	}
	res := e.mustDeploy(contract.SiteDeployParams{Commit: &old})
	if res.Commit != old || !strings.HasSuffix(string(res.Release), string(old[:7])) {
		t.Errorf("result %+v, want commit %s", res, old)
	}
	if got := readFile(t, e.home("site/current/index.html")); got != "old" {
		t.Errorf("index.html = %q, want old", got)
	}
}

// TestBuildFailureRemovesNewReleaseAndKeepsCurrent: a failing Composer or
// deploy script removes the new release and leaves current and the state
// alone.
func TestBuildFailureRemovesNewReleaseAndKeepsCurrent(t *testing.T) {
	for _, c := range []struct {
		name   string
		params contract.SiteDeployParams
		fail   string // the program the hook fails
	}{
		{"composer", contract.SiteDeployParams{Composer: true}, "composer"},
		{"script", contract.SiteDeployParams{Script: "echo half > half.txt\nfalse\necho never\n"}, ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := newDeployEnv(t, contract.SitePHP)
			e.up.commit(map[string]string{"composer.json": "{}", "index.php": "<?php"})
			hook := e.h.hook
			e.h.hook = func(a helper.Account, r helper.ExecRequest) (int, bool) {
				if r.Program == c.fail {
					return 2, true
				}
				return hook(a, r)
			}
			before := e.state()
			_, err := e.deploy(c.params)
			if err == nil {
				t.Fatal("the deploy passed")
			}
			if !slices.Equal(e.releases(), []string{"initial"}) {
				t.Errorf("releases %q; want the new one removed", e.releases())
			}
			if e.current() != initialRel {
				t.Errorf("current -> %s", e.current())
			}
			if st := e.state(); !slices.Equal(st.Releases, before.Releases) {
				t.Errorf("state releases %+v", st.Releases)
			}
			if _, err := os.Stat(e.p.FPMReloadMark("8.4")); !errors.Is(err, fs.ErrNotExist) {
				t.Errorf("a failed deploy marked PHP 8.4: %v", err)
			}
		})
	}
}

func TestComposerSkippedWithoutComposerJSON(t *testing.T) {
	e := newDeployEnv(t, contract.SitePHP)
	e.up.commit(map[string]string{"index.php": "<?php"})
	e.mustDeploy(contract.SiteDeployParams{Composer: true})
	// A static site has no PHP to run Composer with.
	s := newDeployEnv(t, contract.SiteStatic)
	s.up.commit(map[string]string{"composer.json": "{}"})
	s.mustDeploy(contract.SiteDeployParams{Composer: true})
	// Composer off.
	o := newDeployEnv(t, contract.SitePHP)
	o.up.commit(map[string]string{"composer.json": "{}"})
	o.mustDeploy(contract.SiteDeployParams{Composer: false})
	for _, env := range []*deployEnv{e, s, o} {
		for _, r := range env.h.reqs {
			if r.Program == "composer" {
				t.Errorf("Composer ran: %q", r.Args)
			}
		}
		if env.composer != 0 {
			t.Errorf("composer.Ensure ran %d times", env.composer)
		}
	}
}

func TestScriptRunsAsSiteUserWithReleaseAsDir(t *testing.T) {
	e := newDeployEnv(t, contract.SiteStatic)
	e.up.commit(map[string]string{"index.html": "one"})
	const script = "pwd > where.txt\necho from the script\n"
	res := e.mustDeploy(contract.SiteDeployParams{Script: script})
	var found int
	for _, r := range e.h.reqs {
		if r.Program != "deploy-script" {
			continue
		}
		found++
		in, _ := io.ReadAll(r.Stdin)
		if r.Dir != "site/releases/"+string(res.Release) || string(in) != script || len(r.Args) != 0 || len(r.Env) != 0 {
			t.Errorf("deploy-script ran in %q with args %q, env %v and stdin %q", r.Dir, r.Args, r.Env, in)
		}
	}
	if found != 1 {
		t.Fatalf("the script ran %d times, want 1", found)
	}
	if got := readFile(t, e.home("site/current/where.txt")); got != e.home("site/releases/"+string(res.Release))+"\n" {
		t.Errorf("the script ran in %q", got)
	}
	if !slices.Contains(e.logs, "deploy script: from the script") {
		t.Errorf("the script's output isn't in the log:\n%s", strings.Join(e.logs, "\n"))
	}
	// Without a script, nothing runs.
	n := len(e.h.reqs)
	e.up.commit(map[string]string{"index.html": "two"})
	e.mustDeploy(contract.SiteDeployParams{})
	for _, r := range e.h.reqs[n:] {
		if r.Program == "deploy-script" {
			t.Error("an empty script ran")
		}
	}
}

// TestBootstrapCacheTightened: after the script (which runs config:cache),
// bootstrap/cache is 0700 and its files 0600, so www-data can't read the
// cached config's secrets. A symlink in it is left alone.
func TestBootstrapCacheTightened(t *testing.T) {
	e := newDeployEnv(t, contract.SitePHP)
	e.up.commit(map[string]string{"bootstrap/cache/.gitignore": "*\n!.gitignore\n", "bootstrap/app.php": "<?php", "run.sh": "#!/bin/sh\n"})
	res := e.mustDeploy(contract.SiteDeployParams{Script: "echo '<?php return [];' > bootstrap/cache/config.php\nln -s config.php bootstrap/cache/link.php\nmkdir bootstrap/cache/sub\n"})
	rel := e.home("site/releases/" + string(res.Release))
	checkModeOf(t, rel+"/bootstrap/cache", fs.ModeDir|0o700)
	checkModeOf(t, rel+"/bootstrap/cache/config.php", 0o600)
	checkModeOf(t, rel+"/bootstrap/cache/.gitignore", 0o600)
	checkModeOf(t, rel+"/bootstrap/app.php", 0o640)
	if fi, err := os.Lstat(rel + "/bootstrap/cache/link.php"); err != nil || fi.Mode()&fs.ModeSymlink == 0 {
		t.Errorf("link.php: %v, %v", fi, err)
	}
	// A release without bootstrap/cache passes too.
	e.up.git("rm", "-rq", "bootstrap")
	e.up.commit(nil)
	e.mustDeploy(contract.SiteDeployParams{})
}

// TestArchiveWithEscapingLinkRefused: a commit holding a symlink out of
// the release fails the export; the new release is removed.
func TestArchiveWithEscapingLinkRefused(t *testing.T) {
	for _, target := range []string{"/etc", "../..", "public/../../.."} {
		t.Run(target, func(t *testing.T) {
			e := newDeployEnv(t, contract.SiteStatic)
			e.up.commit(map[string]string{"public/index.html": "x", "evil": "-> " + target})
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			_, err := Run(ctx, e.d, e.h, e.lookup, contract.SiteDeployParams{Name: "shop", Trigger: contract.DeployManual})
			if err == nil || !strings.Contains(err.Error(), "symlink") || !strings.Contains(err.Error(), "evil") {
				t.Fatalf("deploy = %v; want the symlink refused by name", err)
			}
			if !slices.Equal(e.releases(), []string{"initial"}) || e.current() != initialRel {
				t.Errorf("releases %q, current %s", e.releases(), e.current())
			}
		})
	}
}

// TestForcePushedBranchDeploys: after a force-push that drops the live
// commit, the fetch and the deploy succeed, and the old release stays until
// pruned.
func TestForcePushedBranchDeploys(t *testing.T) {
	e := newDeployEnv(t, contract.SiteStatic)
	e.up.commit(map[string]string{"index.html": "one"})
	first := e.mustDeploy(contract.SiteDeployParams{})
	e.up.git("checkout", "-q", "--orphan", "rewritten")
	e.up.git("rm", "-rqf", ".")
	rewritten := e.up.commit(map[string]string{"index.html": "rewritten"})
	e.up.git("branch", "-M", "rewritten", "main")
	second := e.mustDeploy(contract.SiteDeployParams{})
	if second.Commit != rewritten || second.PreviousRelease == nil || *second.PreviousRelease != first.Release {
		t.Errorf("result %+v; want commit %s after %s", second, rewritten, first.Release)
	}
	if got := readFile(t, e.home("site/current/index.html")); got != "rewritten" {
		t.Errorf("index.html = %q", got)
	}
	if !slices.Contains(e.releases(), string(first.Release)) {
		t.Errorf("the old release is gone: %q", e.releases())
	}
}

// TestHalfBuiltReleaseNeverActivated: a Composer failure from a full disk,
// whose cleanup fails too, leaves current on the old release; the next
// deploy's prune removes the half-built one.
func TestHalfBuiltReleaseNeverActivated(t *testing.T) {
	e := newDeployEnv(t, contract.SitePHP)
	sha := e.up.commit(map[string]string{"composer.json": "{}"})
	half := "site/releases/20261115120000-" + string(sha[:7])
	hook := e.h.hook
	e.h.hook = func(a helper.Account, r helper.ExecRequest) (int, bool) {
		if r.Program == "composer" {
			r.Log("file_put_contents(): Write of 8192 bytes failed with errno=28 No space left on device")
			return 1, true
		}
		return hook(a, r)
	}
	e.h.failRemove = map[string]error{half: errors.New("remove-tree: no space left on device")}
	if _, err := e.deploy(contract.SiteDeployParams{Composer: true}); err == nil {
		t.Fatal("the deploy passed")
	}
	if e.current() != initialRel {
		t.Errorf("current -> %s", e.current())
	}
	if !slices.Contains(e.releases(), filepath.Base(half)) {
		t.Fatalf("the half-built release is gone already: %q", e.releases())
	}
	if st := e.state(); len(st.Releases) != 0 {
		t.Errorf("state releases %+v", st.Releases)
	}

	e.h.hook, e.h.failRemove = hook, nil
	e.now = e.now.Add(time.Minute)
	res := e.mustDeploy(contract.SiteDeployParams{Composer: true})
	if want := []string{string(res.Release), "initial"}; !slices.Equal(e.releases(), want) {
		t.Errorf("releases %q, want %q", e.releases(), want)
	}
}

// TestRedeployInSameSecondDeletesNothing: the same commit deployed twice in
// one second gets the live release's id; the second deploy fails and
// removes nothing.
func TestRedeployInSameSecondDeletesNothing(t *testing.T) {
	e := newDeployEnv(t, contract.SiteStatic)
	e.up.commit(map[string]string{"index.html": "one"})
	res, err := e.deploy(contract.SiteDeployParams{})
	wantNoErr(t, err)
	if _, err := e.deploy(contract.SiteDeployParams{}); err == nil {
		t.Fatal("the second deploy in the same second passed")
	}
	if e.current() != "releases/"+string(res.Release) {
		t.Errorf("current -> %s", e.current())
	}
	if got := readFile(t, e.home("site/current/index.html")); got != "one" {
		t.Errorf("the live release lost its files: %q", got)
	}
	for _, op := range e.h.ops {
		if op == "remove-tree site/releases/"+string(res.Release) {
			t.Error("the live release was removed")
		}
	}
}

// TestPruneKeepsActiveAndPrevious: beyond keep_releases, the oldest
// releases go, and with them the placeholder and anything the state
// doesn't list; the live one and the one before it always stay.
func TestPruneKeepsActiveAndPrevious(t *testing.T) {
	e := newDeployEnv(t, contract.SiteStatic)
	one := contract.KeepReleases(1)
	var res []contract.SiteDeployResult
	for i := range 3 {
		e.up.commit(map[string]string{"index.html": fmt.Sprint(i)})
		params := contract.SiteDeployParams{}
		if i == 1 {
			params.KeepReleases = &one // stored, and used from then on
		}
		res = append(res, e.mustDeploy(params))
	}
	if want := []string{string(res[1].Release), string(res[2].Release)}; !slices.Equal(e.releases(), want) {
		t.Errorf("releases %q, want %q", e.releases(), want)
	}
	st := e.state()
	if st.KeepReleases != 1 || len(st.Releases) != 2 || st.Releases[0].ID != res[1].Release || st.Releases[0].Active || !st.Releases[1].Active {
		t.Errorf("state keep %d, releases %+v", st.KeepReleases, st.Releases)
	}

	// After a rollback to an old release (as site.rollback leaves it), the
	// next deploy keeps that one as the previous, however old.
	if err := os.Symlink("releases/"+string(res[1].Release), e.home("site/current.tmp")); err != nil {
		t.Fatal(err)
	}
	wantNoErr(t, os.Rename(e.home("site/current.tmp"), e.home("site/current")))
	st.Releases[0].Active, st.Releases[1].Active = true, false
	wantNoErr(t, sites.Save(e.p, st))
	e.up.commit(map[string]string{"index.html": "3"})
	last := e.mustDeploy(contract.SiteDeployParams{})
	if last.PreviousRelease == nil || *last.PreviousRelease != res[1].Release {
		t.Errorf("previous release %v, want %s", last.PreviousRelease, res[1].Release)
	}
	if want := []string{string(res[1].Release), string(last.Release)}; !slices.Equal(e.releases(), want) {
		t.Errorf("releases %q, want %q", e.releases(), want)
	}
}

func TestPruneSet(t *testing.T) {
	rel := func(id string) contract.Release { return contract.Release{ID: contract.ReleaseID(id)} }
	state := []contract.Release{rel("20260101000000-aaaaaaa"), rel("20260102000000-bbbbbbb"), rel("20260103000000-ccccccc"), rel("20260104000000-ddddddd")}
	for _, c := range []struct {
		keep             contract.KeepReleases
		active, previous string
		want             []string
	}{
		{1, "20260104000000-ddddddd", "20260103000000-ccccccc", []string{"20260103000000-ccccccc", "20260104000000-ddddddd"}},
		{2, "20260104000000-ddddddd", "initial", []string{"20260103000000-ccccccc", "20260104000000-ddddddd", "initial"}},
		{1, "20260104000000-ddddddd", "20260101000000-aaaaaaa", []string{"20260101000000-aaaaaaa", "20260104000000-ddddddd"}},
		{5, "20260104000000-ddddddd", "", []string{"20260101000000-aaaaaaa", "20260102000000-bbbbbbb", "20260103000000-ccccccc", "20260104000000-ddddddd"}},
	} {
		got := keptReleases(state, c.keep, c.active, c.previous)
		names := slices.Sorted(func(yield func(string) bool) {
			for n := range got {
				if !yield(n) {
					return
				}
			}
		})
		if !slices.Equal(names, c.want) {
			t.Errorf("keptReleases(keep %d, active %s, previous %s) = %q, want %q", c.keep, c.active, c.previous, names, c.want)
		}
	}
}

// TestDeployMarksPHPVersion (spike amendment A3): a PHP site's deploy marks
// its version for maintenance's graceful reload, root 0600, and reloads
// nothing itself; a static site marks nothing.
func TestDeployMarksPHPVersion(t *testing.T) {
	e := newDeployEnv(t, contract.SitePHP)
	e.up.commit(map[string]string{"index.php": "<?php"})
	e.mustDeploy(contract.SiteDeployParams{})
	checkModeOf(t, e.p.FPMReloadMark("8.4"), 0o600)
	checkModeOf(t, e.p.FPMReloadDir(), fs.ModeDir|0o700)
	if calls := e.d.Run.(*sysexec.FakeRunner).Calls(); len(calls) != 0 {
		t.Errorf("the deploy ran %v; want nothing as root", calls)
	}
	s := newDeployEnv(t, contract.SiteStatic)
	s.up.commit(map[string]string{"index.html": "x"})
	s.mustDeploy(contract.SiteDeployParams{})
	if _, err := os.Stat(s.p.FPMReloadDir()); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("a static site's deploy made %s: %v", s.p.FPMReloadDir(), err)
	}
}

// TestDeployOfSiteWithoutRepoIsConflict: the precheck answers conflict for
// a site without a repository and not_found for one that doesn't exist.
func TestDeployOfSiteWithoutRepoIsConflict(t *testing.T) {
	p := paths.Paths{Root: t.TempDir()}
	d := sysexec.Deps{Paths: p, Run: sysexec.NewFakeRunner(), Logf: t.Logf, Now: time.Now}
	params := contract.SiteDeployParams{Name: "shop", Trigger: contract.DeployManual}
	var ae *contract.AgentError
	if err := CheckDeploy(d, params); !errors.As(err, &ae) || ae.Code != contract.ErrNotFound {
		t.Errorf("CheckDeploy of a missing site = %v, want not_found", err)
	}
	wantNoErr(t, sites.Save(p, sites.State{Name: "shop", Domain: "shop.example.com", Type: contract.SiteStatic, TemplateVersion: 1, Security: sites.DefaultSecurity()}))
	if err := CheckDeploy(d, params); !errors.As(err, &ae) || ae.Code != contract.ErrConflict {
		t.Errorf("CheckDeploy without a repository = %v, want conflict", err)
	}
	h := &localHelper{t: t}
	if _, err := Run(context.Background(), d, h, func(string) (helper.Account, error) { return helper.Account{}, errors.New("no lookup") }, params); !errors.As(err, &ae) || ae.Code != contract.ErrConflict {
		t.Errorf("Run without a repository = %v, want conflict", err)
	}
	if len(h.ops) != 0 {
		t.Errorf("Run touched the home: %q", h.ops)
	}
}

// eofOnError turns a read error into an early end, as a process reading a
// pipe sees it.
type eofOnError struct{ r io.Reader }

func (e eofOnError) Read(p []byte) (int, error) {
	n, err := e.r.Read(p)
	if err != nil && err != io.EOF {
		err = io.EOF
	}
	return n, err
}
