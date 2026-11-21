package siteprocs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"zimam/agent/contract"
	"zimam/agent/internal/helper"
	"zimam/agent/internal/sites"
	"zimam/agent/internal/templates"
)

// homeHelper runs the helper's real operations (helper.Serve) inside a
// temporary home as an os.Root, so what the scripts do to the home, including
// refusing a symlink that leaves it, is the helper's own behaviour. Exec only
// records the request.
type homeHelper struct {
	root  *os.Root
	home  string
	exec  []helper.ExecRequest
	reads []string // every path Read was asked for
	ops   []string // "<op> <path>" of every operation but exec
	acct  []string
}

func newHome(t *testing.T) *homeHelper {
	t.Helper()
	home := t.TempDir()
	root, err := os.OpenRoot(home)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.Close() })
	return &homeHelper{root: root, home: home}
}

func (h *homeHelper) serve(a helper.Account, op string, stdin io.Reader, out any, args ...string) error {
	h.acct = append(h.acct, a.Name)
	h.ops = append(h.ops, op+" "+args[0])
	var stdout, stderr bytes.Buffer
	if stdin == nil {
		stdin = strings.NewReader("")
	}
	switch code := helper.Serve(h.root, op, args, stdin, &stdout, &stderr); code {
	case 0:
		if out != nil {
			return contract.DecodeStrict(bytes.TrimSpace(stdout.Bytes()), out)
		}
		return nil
	case 3:
		return fs.ErrNotExist
	default:
		return errors.New(strings.TrimSpace(stderr.String()))
	}
}

func (h *homeHelper) Mkdir(_ context.Context, a helper.Account, name string, mode fs.FileMode) error {
	return h.serve(a, "mkdir", nil, nil, name, fmt.Sprintf("%o", mode))
}

func (h *homeHelper) Exec(_ context.Context, _ helper.Account, r helper.ExecRequest) (int, error) {
	h.exec = append(h.exec, r)
	return 0, nil
}

func (h *homeHelper) Write(_ context.Context, a helper.Account, name string, mode fs.FileMode, c io.Reader) error {
	return h.serve(a, "write", c, nil, name, fmt.Sprintf("%o", mode))
}

func (h *homeHelper) List(_ context.Context, a helper.Account, dir string) (helper.Listing, error) {
	var l helper.Listing
	err := h.serve(a, "list", nil, &l, dir)
	return l, err
}

func (h *homeHelper) RemoveTree(_ context.Context, a helper.Account, name string) error {
	return h.serve(a, "remove-tree", nil, nil, name)
}

func (h *homeHelper) Read(_ context.Context, a helper.Account, name string, offset, limit int64) (helper.ReadResult, error) {
	h.reads = append(h.reads, name)
	var r helper.ReadResult
	err := h.serve(a, "read", nil, &r, name, strconv.FormatInt(offset, 10), strconv.FormatInt(limit, 10))
	return r, err
}

var acct = helper.Account{Name: "s_shop", UID: 1001, GID: 1001, Home: "/home/s_shop"}

func state(php contract.PHPVersion) sites.State {
	return sites.State{Name: "shop", Type: contract.SiteLaravel, PHPVersion: php}
}

func (h *homeHelper) read(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(h.home, rel))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func (h *homeHelper) mode(t *testing.T, rel string) fs.FileMode {
	t.Helper()
	fi, err := os.Stat(filepath.Join(h.home, rel))
	if err != nil {
		t.Fatal(err)
	}
	return fi.Mode().Perm()
}

func (h *homeHelper) names(t *testing.T, rel string) []string {
	t.Helper()
	es, err := os.ReadDir(filepath.Join(h.home, rel))
	if err != nil {
		t.Fatal(err)
	}
	var n []string
	for _, e := range es {
		n = append(n, e.Name())
	}
	return n
}

func ptr(n int) *int { return &n }

func TestQueueWorkArgv(t *testing.T) {
	full := contract.WorkerSpec{ID: "q", Kind: contract.WorkerLaravelQueue, Processes: 2, MemoryMB: 256, Connection: "redis",
		Queues: []contract.QueueName{"high", "default"}, Tries: ptr(3), Timeout: ptr(0), Sleep: ptr(3), MaxTime: ptr(3600), Backoff: ptr(5)}
	want := []string{"php", "artisan", "queue:work", "redis", "--queue=high,default", "--tries=3", "--timeout=0", "--sleep=3", "--max-time=3600", "--backoff=5", "--memory=230"}
	if got := workerScript(full).Argv; !slices.Equal(got, want) {
		t.Errorf("argv = %q, want %q", got, want)
	}
	// Nothing set: only the defaults the agent applies.
	bare := contract.WorkerSpec{ID: "q", Kind: contract.WorkerLaravelQueue, Processes: 1}
	want = []string{"php", "artisan", "queue:work", "default", "--memory=460"}
	if got := workerScript(bare).Argv; !slices.Equal(got, want) {
		t.Errorf("argv = %q, want %q", got, want)
	}
}

func TestScriptsWrittenThroughHelperAsSiteUser(t *testing.T) {
	h := newHome(t)
	workers := []contract.WorkerSpec{
		{ID: "queue", Kind: contract.WorkerLaravelQueue, Processes: 1, Connection: "redis", Queues: []contract.QueueName{"high"}},
		{ID: "ws", Kind: contract.WorkerCustom, Processes: 1, Command: "node ws.js | cat"},
	}
	if err := WriteWorkerScripts(context.Background(), h, acct, state("8.4"), workers); err != nil {
		t.Fatal(err)
	}
	cron := contract.SiteCronParams{Name: "shop", Scheduler: true, Jobs: contract.CronJobList{
		{ID: "backup", Schedule: "0 3 * * *", Command: "php artisan backup:run", Enabled: true},
	}}
	if err := WriteCronScripts(context.Background(), h, acct, state("8.4"), cron); err != nil {
		t.Fatal(err)
	}
	if len(h.acct) == 0 {
		t.Fatal("no helper operation ran")
	}
	for _, a := range h.acct {
		if a != "s_shop" {
			t.Errorf("an operation ran as %q", a)
		}
	}
	for _, f := range []string{".zimam/workers/queue.sh", ".zimam/workers/ws.sh", ".zimam/cron/backup.sh", ".zimam/cron/scheduler.sh"} {
		if m := h.mode(t, f); m != 0o700 {
			t.Errorf("%s mode %o, want 700", f, m)
		}
	}
	for _, d := range []string{".zimam", ".zimam/workers", ".zimam/cron"} {
		if m := h.mode(t, d); m != 0o700 {
			t.Errorf("%s mode %o, want 700", d, m)
		}
	}
	if m := h.mode(t, ".zimam/site.env.sh"); m != 0o600 {
		t.Errorf("site.env.sh mode %o, want 600", m)
	}
	if got := h.read(t, ".zimam/workers/queue.sh"); !strings.Contains(got, "exec 'php' 'artisan' 'queue:work' 'redis' '--queue=high' '--memory=460'\n") {
		t.Errorf("queue.sh:\n%s", got)
	}
	// The owner's command is a line of its own, with no exec added.
	if got := h.read(t, ".zimam/workers/ws.sh"); !strings.HasSuffix(got, "\nnode ws.js | cat\n") {
		t.Errorf("ws.sh:\n%s", got)
	}
	if got := h.read(t, ".zimam/cron/scheduler.sh"); !strings.Contains(got, "\nphp artisan schedule:run\n") {
		t.Errorf("scheduler.sh:\n%s", got)
	}
}

func TestStaleScriptsRemoved(t *testing.T) {
	h := newHome(t)
	ctx := context.Background()
	w := func(ids ...contract.WorkerID) []contract.WorkerSpec {
		var l []contract.WorkerSpec
		for _, id := range ids {
			l = append(l, contract.WorkerSpec{ID: id, Kind: contract.WorkerCustom, Processes: 1, Command: "x"})
		}
		return l
	}
	if err := WriteWorkerScripts(ctx, h, acct, state(""), w("a", "b")); err != nil {
		t.Fatal(err)
	}
	if err := WriteWorkerScripts(ctx, h, acct, state(""), w("b")); err != nil {
		t.Fatal(err)
	}
	if got := h.names(t, ".zimam/workers"); !slices.Equal(got, []string{"b.sh"}) {
		t.Errorf("workers: %v", got)
	}
	if err := WriteWorkerScripts(ctx, h, acct, state(""), nil); err != nil {
		t.Fatal(err)
	}
	if got := h.names(t, ".zimam/workers"); len(got) != 0 {
		t.Errorf("workers: %v", got)
	}

	job := func(id contract.WorkerID, on bool) contract.CronJobSpec {
		return contract.CronJobSpec{ID: id, Schedule: "* * * * *", Command: "x", Enabled: on}
	}
	all := contract.SiteCronParams{Name: "shop", Scheduler: true, Jobs: contract.CronJobList{job("a", true), job("b", true), job("off", false)}}
	if err := WriteCronScripts(ctx, h, acct, state(""), all); err != nil {
		t.Fatal(err)
	}
	if err := PruneCronScripts(ctx, h, acct, all); err != nil {
		t.Fatal(err)
	}
	if got := h.names(t, ".zimam/cron"); !slices.Equal(got, []string{"a.sh", "b.sh", "scheduler.sh"}) {
		t.Errorf("cron after all: %v (a disabled job has no script)", got)
	}
	// A lock file stays; the scheduler goes with its switch, b with its row.
	if err := os.WriteFile(filepath.Join(h.home, ".zimam/cron/b.lock"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	some := contract.SiteCronParams{Name: "shop", Jobs: contract.CronJobList{job("a", true)}}
	if err := WriteCronScripts(ctx, h, acct, state(""), some); err != nil {
		t.Fatal(err)
	}
	if err := PruneCronScripts(ctx, h, acct, some); err != nil {
		t.Fatal(err)
	}
	if got := h.names(t, ".zimam/cron"); !slices.Equal(got, []string{"a.sh", "b.lock"}) {
		t.Errorf("cron after some: %v", got)
	}
}

func TestSiteEnvUsesSitePHPVersion(t *testing.T) {
	h := newHome(t)
	if err := WriteSiteEnv(context.Background(), h, acct, state("8.3")); err != nil {
		t.Fatal(err)
	}
	if got := h.read(t, ".zimam/site.env.sh"); !strings.Contains(got, `export PATH="$HOME/.zimam/php8.3:/usr/local/bin:/usr/bin:/bin"`+"\n") {
		t.Errorf("site.env.sh:\n%s", got)
	}
	// The helper's exec makes the php directory for that version.
	if len(h.exec) != 1 || h.exec[0].Program != "php" || h.exec[0].PHP != "8.3" {
		t.Errorf("exec requests: %+v", h.exec)
	}
	// A static site has no php directory, and nothing to make.
	h2 := newHome(t)
	if err := WriteSiteEnv(context.Background(), h2, acct, state("")); err != nil {
		t.Fatal(err)
	}
	if got := h2.read(t, ".zimam/site.env.sh"); strings.Contains(got, "php") || len(h2.exec) != 0 {
		t.Errorf("static site: %q, %d execs", got, len(h2.exec))
	}
}

// Each half writes site.env.sh first (ledger R3).
func TestEveryWriteStartsWithSiteEnv(t *testing.T) {
	for name, run := range map[string]func(h *homeHelper) error{
		"workers": func(h *homeHelper) error {
			return WriteWorkerScripts(context.Background(), h, acct, state("8.4"), nil)
		},
		"cron": func(h *homeHelper) error {
			return WriteCronScripts(context.Background(), h, acct, state("8.4"), contract.SiteCronParams{Name: "shop", Jobs: contract.CronJobList{}})
		},
	} {
		h := newHome(t)
		if err := run(h); err != nil {
			t.Fatal(err)
		}
		if h.read(t, ".zimam/site.env.sh") == "" || len(h.exec) != 1 {
			t.Errorf("%s: no site.env.sh", name)
		}
	}
}

// A planted .zimam -> /etc must fail inside the helper's os.Root, and nothing
// is written through it.
func TestScriptDirRefusesSymlink(t *testing.T) {
	h := newHome(t)
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(h.home, ".zimam")); err != nil {
		t.Fatal(err)
	}
	err := WriteWorkerScripts(context.Background(), h, acct, state("8.4"), []contract.WorkerSpec{{ID: "a", Kind: contract.WorkerCustom, Processes: 1, Command: "x"}})
	if err == nil {
		t.Fatal("wrote through a .zimam symlink")
	}
	if es, _ := os.ReadDir(outside); len(es) != 0 {
		t.Errorf("wrote %d entries outside the home", len(es))
	}
}

// cronHome makes a home for running rendered cron scripts under the real bash
// and flock: script writes one and returns its path, run makes its command,
// and log is the cron log.
func cronHome(t *testing.T) (script func(id, cmd string) string, run func(p string) *exec.Cmd, log string) {
	t.Helper()
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("no bash")
	}
	if _, err := exec.LookPath("flock"); err != nil {
		t.Skip("no flock")
	}
	home := t.TempDir()
	for _, d := range []string{".zimam/cron", "logs", "site/current"} {
		if err := os.MkdirAll(filepath.Join(home, d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	env, err := templates.Render("site/site.env.sh", templates.SiteEnv{})
	if err != nil {
		t.Fatal(err)
	}
	write := func(name string, b []byte) string {
		p := filepath.Join(home, name)
		if err := os.WriteFile(p, b, 0o700); err != nil {
			t.Fatal(err)
		}
		return p
	}
	write(".zimam/site.env.sh", env)
	script = func(id, cmd string) string {
		b, err := templates.Render("site/cron.sh", templates.CronScript{ID: contract.WorkerID(id), Command: contract.OwnerCommand(cmd)})
		if err != nil {
			t.Fatal(err)
		}
		return write(".zimam/cron/"+id+".sh", b)
	}
	run = func(p string) *exec.Cmd {
		c := exec.Command(bash, p)
		c.Env = []string{"HOME=" + home, "PATH=/usr/local/bin:/usr/bin:/bin"}
		return c
	}
	return script, run, filepath.Join(home, "logs/cron.log")
}

// waitForLog waits until the cron log holds want.
func waitForLog(t *testing.T, log, want string) {
	t.Helper()
	for i := 0; ; i++ {
		if b, _ := os.ReadFile(log); strings.Contains(string(b), want) {
			return
		}
		if i > 100 {
			t.Fatalf("the log never had %q", want)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// A second run while the first holds the lock exits 0 and logs the skip; a
// failing job reports its status. This runs the rendered script under real bash.
func TestCronScriptSkipsWhileRunning(t *testing.T) {
	script, run, log := cronHome(t)
	slowPath := script("slow", "echo working; sleep 3")
	slow := run(slowPath)
	if err := slow.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { slow.Process.Kill(); slow.Wait() })
	waitForLog(t, log, "[slow] started")
	if err := run(slowPath).Run(); err != nil {
		t.Fatalf("the overlapping run: %v (want exit 0)", err)
	}
	b, _ := os.ReadFile(log)
	if n := strings.Count(string(b), "[slow] skipped: the previous run is still going"); n != 1 {
		t.Errorf("%d skip lines:\n%s", n, b)
	}
	if err := slow.Wait(); err != nil {
		t.Fatalf("the first run: %v", err)
	}
	b, _ = os.ReadFile(log)
	if !strings.Contains(string(b), "working\n") || !strings.Contains(string(b), "[slow] finished with status 0") {
		t.Errorf("the first run's log:\n%s", b)
	}
	err := run(script("bad", "echo oops; exit 7")).Run()
	var ee *exec.ExitError
	if !errors.As(err, &ee) || ee.ExitCode() != 7 {
		t.Errorf("bad job: %v, want exit 7", err)
	}
	if b, _ = os.ReadFile(log); !strings.Contains(string(b), "[bad] finished with status 7") {
		t.Errorf("log:\n%s", b)
	}
}

// A child the command leaves running (Laravel's runInBackground, nohup ... &)
// must not keep the job's lock, or every later run is skipped.
func TestCronLockNotKeptByBackgroundChild(t *testing.T) {
	script, run, log := cronHome(t)
	p := script("bg", "sleep 3 >/dev/null 2>&1 &")
	for i := 1; i <= 2; i++ {
		if err := run(p).Run(); err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
	}
	b, _ := os.ReadFile(log)
	if strings.Contains(string(b), "skipped") || strings.Count(string(b), "[bg] started") != 2 {
		t.Errorf("the second run didn't run:\n%s", b)
	}
}

// The scheduler takes no lock (ruling R8): Laravel wants schedule:run every
// minute and handles overlap per task, so a long task from an earlier minute
// must not skip the tasks due now.
func TestSchedulerRunsWhileRunning(t *testing.T) {
	script, run, log := cronHome(t)
	p := script(SchedulerID, "echo working; sleep 2")
	first := run(p)
	if err := first.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { first.Process.Kill(); first.Wait() })
	waitForLog(t, log, "[scheduler] started")
	if err := run(p).Run(); err != nil {
		t.Fatalf("the overlapping run: %v", err)
	}
	b, _ := os.ReadFile(log)
	if strings.Contains(string(b), "skipped") || strings.Count(string(b), "[scheduler] started") != 2 {
		t.Errorf("the overlapping run was skipped:\n%s", b)
	}
}
