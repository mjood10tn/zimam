package siteprocs

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"zimam/agent/contract"
	"zimam/agent/internal/paths"
	"zimam/agent/internal/sites"
	"zimam/agent/internal/sysexec"
	"zimam/agent/internal/systemd"
)

// unitRunner stands in for systemctl and systemd-analyze. It records every
// call, answers show with a block per unit asked (props[unit], or an active
// running unit), and fails verify while badVerify is set.
type unitRunner struct {
	calls     [][]string
	props     map[string]map[string]string
	badVerify bool
	started   string // every process's ExecMainStartTimestamp, unless props says
	failOnce  string // the next systemctl call starting with these arguments fails
}

func (r *unitRunner) Run(_ context.Context, c sysexec.Cmd) (sysexec.Result, error) {
	r.calls = append(r.calls, append([]string{c.Path}, c.Args...))
	if args := strings.Join(c.Args, " "); c.Path == systemctl && r.failOnce != "" && strings.HasPrefix(args, r.failOnce) {
		r.failOnce = ""
		return sysexec.Result{}, &sysexec.ExitError{Path: c.Path, Code: 1}
	}
	switch {
	case c.Path == systemdAnalyze && r.badVerify:
		return sysexec.Result{Stderr: []byte("Unknown key 'Bogus'")}, &sysexec.ExitError{Path: c.Path, Code: 1}
	case c.Path == systemctl && c.Args[0] == "show":
		units := c.Args[slices.Index(c.Args, "--")+1:]
		var b strings.Builder
		for _, u := range units {
			p := map[string]string{"ActiveState": "active", "SubState": "running", "NRestarts": "0", "Result": "success", "ExecMainStatus": "0", "StateChangeTimestamp": "@1763460000", "ExecMainStartTimestamp": r.started}
			for k, v := range r.props[u] {
				p[k] = v
			}
			b.WriteString("Id=" + u + "\n")
			for _, k := range []string{"ActiveState", "SubState", "NRestarts", "StateChangeTimestamp", "Result", "ExecMainStatus", "ExecMainStartTimestamp"} {
				b.WriteString(k + "=" + p[k] + "\n")
			}
			b.WriteString("\n")
		}
		return sysexec.Result{Stdout: []byte(b.String())}, nil
	}
	return sysexec.Result{}, nil
}

// systemctl returns the systemctl calls but show, each as its arguments.
func (r *unitRunner) systemctl() []string {
	var out []string
	for _, c := range r.calls {
		if c[0] == systemctl && c[1] != "show" {
			out = append(out, strings.Join(c[1:], " "))
		}
	}
	return out
}

func (r *unitRunner) reset() { r.calls = nil }

func fullState() sites.State {
	return sites.State{Name: "shop", Domain: "shop.example.com", Type: contract.SiteLaravel, PHPVersion: "8.4", TemplateVersion: 3, Security: sites.DefaultSecurity()}
}

type fixture struct {
	d   sysexec.Deps
	run *unitRunner
	h   *homeHelper
	st  sites.State
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	run := &unitRunner{props: map[string]map[string]string{}}
	f := &fixture{d: sysexec.Deps{Paths: paths.Paths{Root: t.TempDir()}, Run: run, Logf: t.Logf, Now: time.Now}, run: run, h: newHome(t), st: fullState()}
	if err := os.MkdirAll(f.d.Paths.SystemdUnitDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := sites.Save(f.d.Paths, f.st); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *fixture) reconcile(t *testing.T, ws ...contract.WorkerSpec) error {
	t.Helper()
	st, err := Reconcile(context.Background(), f.d, f.h, acct, f.st, ws)
	if err == nil {
		f.st = st
	}
	return err
}

func queue(processes, memory int) contract.WorkerSpec {
	return contract.WorkerSpec{ID: "q", Kind: contract.WorkerLaravelQueue, Processes: processes, MemoryMB: contract.WorkerMemoryMB(memory)}
}

func (f *fixture) unit(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(f.d.Paths.SystemdUnit(name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func has(calls []string, want string) bool { return slices.Contains(calls, want) }

// settle is time passing after a run: every unit file and script is a
// minute and a half old, and every process started a minute ago, after
// them. A later run that rewrites one makes it newer than its process.
func (f *fixture) settle(t *testing.T) {
	t.Helper()
	old := time.Now().Add(-90 * time.Second)
	for _, dir := range []string{f.d.Paths.SystemdUnitDir(), filepath.Join(f.h.home, ".zimam/workers")} {
		es, _ := os.ReadDir(dir)
		for _, e := range es {
			if err := os.Chtimes(filepath.Join(dir, e.Name()), old, old); err != nil {
				t.Fatal(err)
			}
		}
	}
	f.run.started = "@" + strconv.FormatInt(time.Now().Add(-time.Minute).Unix(), 10)
}

func surplusOf(id contract.WorkerID, from int) string {
	return "disable --now --no-block " + strings.Join(systemd.WorkerInstances("shop", id, from, 10), " ")
}

func TestReconcileStartsNewStopsRemoved(t *testing.T) {
	f := newFixture(t)
	if err := f.reconcile(t, contract.WorkerSpec{ID: "old", Kind: contract.WorkerCustom, Processes: 1, Command: "sleep 9"}); err != nil {
		t.Fatal(err)
	}
	f.run.reset()
	if err := f.reconcile(t, queue(2, 512)); err != nil {
		t.Fatal(err)
	}
	calls := f.run.systemctl()
	want := []string{
		surplusOf("old", 1),
		"daemon-reload",
		"enable zimam-site-shop.target zimam-worker-shop_q@1.service zimam-worker-shop_q@2.service",
		"start --no-block zimam-site-shop.target zimam-worker-shop_q@1.service zimam-worker-shop_q@2.service",
	}
	if !slices.Equal(calls, want) {
		t.Errorf("systemctl calls:\n%s\nwant:\n%s", strings.Join(calls, "\n"), strings.Join(want, "\n"))
	}
	if _, err := os.Stat(f.d.Paths.SystemdUnit("zimam-worker-shop_old@.service")); !os.IsNotExist(err) {
		t.Errorf("old's unit is still there: %v", err)
	}
	if u := f.unit(t, "zimam-worker-shop_q@.service"); !strings.Contains(u, "ExecStart=/bin/bash /home/s_shop/.zimam/workers/q.sh\n") {
		t.Errorf("q's unit:\n%s", u)
	}
	if got := f.h.names(t, ".zimam/workers"); !slices.Equal(got, []string{"q.sh"}) {
		t.Errorf("scripts: %v", got)
	}
	// The verify ran on the target and the new unit, with the strict flag.
	var verify []string
	for _, c := range f.run.calls {
		if c[0] == systemdAnalyze {
			verify = c[1:]
		}
	}
	if len(verify) != 4 || verify[0] != "verify" || verify[1] != "--recursive-errors=no" || filepath.Base(verify[3]) != "zimam-worker-shop_q@.service" {
		t.Errorf("verify = %q", verify)
	}
}

func restartsIn(calls []string) []string {
	var out []string
	for _, c := range calls {
		if strings.HasPrefix(c, "restart") {
			out = append(out, c)
		}
	}
	return out
}

func TestUnchangedWorkerNotRestarted(t *testing.T) {
	f := newFixture(t)
	if err := f.reconcile(t, queue(2, 512)); err != nil {
		t.Fatal(err)
	}
	f.settle(t)
	f.run.reset()
	if err := f.reconcile(t, queue(2, 512)); err != nil {
		t.Fatal(err)
	}
	if r := restartsIn(f.run.systemctl()); len(r) != 0 {
		t.Errorf("an unchanged worker got %q", r)
	}
	for _, c := range f.run.calls {
		if c[0] == systemdAnalyze {
			t.Error("nothing changed, but verify ran")
		}
	}
	// A changed limit restarts it, by name.
	f.run.reset()
	if err := f.reconcile(t, queue(2, 1024)); err != nil {
		t.Fatal(err)
	}
	if calls := f.run.systemctl(); !has(calls, "restart --no-block zimam-worker-shop_q@1.service zimam-worker-shop_q@2.service") {
		t.Errorf("a changed worker wasn't restarted: %q", calls)
	}
}

func TestChangedCustomCommandRestarts(t *testing.T) {
	f := newFixture(t)
	w := contract.WorkerSpec{ID: "h", Kind: contract.WorkerCustom, Processes: 1, Command: "php artisan horizon"}
	if err := f.reconcile(t, w); err != nil {
		t.Fatal(err)
	}
	f.settle(t)
	f.run.reset()
	w.Command = "php artisan horizon --quiet"
	if err := f.reconcile(t, w); err != nil {
		t.Fatal(err)
	}
	if calls := f.run.systemctl(); !has(calls, "restart --no-block zimam-worker-shop_h@1.service") {
		t.Errorf("a changed script wasn't restarted: %q", calls)
	}
}

// A run that rewrote a unit or a script and failed before reloading and
// restarting is finished by the retry, which finds nothing left to write.
func TestRetryAfterFailureReloadsAndRestarts(t *testing.T) {
	for name, c := range map[string]struct{ before, after contract.WorkerSpec }{
		"unit":   {queue(1, 512), queue(1, 1024)},
		"script": {contract.WorkerSpec{ID: "q", Kind: contract.WorkerCustom, Processes: 1, Command: "a"}, contract.WorkerSpec{ID: "q", Kind: contract.WorkerCustom, Processes: 1, Command: "b"}},
	} {
		f := newFixture(t)
		if err := f.reconcile(t, c.before); err != nil {
			t.Fatal(err)
		}
		f.settle(t)
		f.run.failOnce = "daemon-reload"
		if err := f.reconcile(t, c.after); err == nil {
			t.Fatalf("%s: the failed reload passed", name)
		}
		f.run.reset()
		if err := f.reconcile(t, c.after); err != nil {
			t.Fatal(err)
		}
		calls := f.run.systemctl()
		if !has(calls, "daemon-reload") || !has(calls, "restart --no-block zimam-worker-shop_q@1.service") {
			t.Errorf("%s: the retry ran %q; want a reload and the restart", name, calls)
		}
	}
}

func TestProcessesCountChangesInstances(t *testing.T) {
	f := newFixture(t)
	if err := f.reconcile(t, queue(3, 512)); err != nil {
		t.Fatal(err)
	}
	f.run.reset()
	if err := f.reconcile(t, queue(1, 512)); err != nil {
		t.Fatal(err)
	}
	calls := f.run.systemctl()
	if !has(calls, surplusOf("q", 2)) || !has(calls, "start --no-block zimam-site-shop.target zimam-worker-shop_q@1.service") {
		t.Errorf("shrinking: %q", calls)
	}
	f.run.reset()
	if err := f.reconcile(t, queue(3, 512)); err != nil {
		t.Fatal(err)
	}
	if calls := f.run.systemctl(); !has(calls, surplusOf("q", 4)) || !has(calls, "start --no-block zimam-site-shop.target zimam-worker-shop_q@1.service zimam-worker-shop_q@2.service zimam-worker-shop_q@3.service") {
		t.Errorf("growing: %q", calls)
	}
}

// A run that started more processes and failed before recording them: the
// retry with the old count still stops them, though the state never said.
func TestRetryStopsUnrecordedInstances(t *testing.T) {
	f := newFixture(t)
	if err := f.reconcile(t, queue(2, 512)); err != nil {
		t.Fatal(err)
	}
	f.run.failOnce = "start --no-block"
	if err := f.reconcile(t, queue(5, 512)); err == nil {
		t.Fatal("the failed start passed")
	}
	if st, _ := sites.Load(f.d.Paths, "shop"); st.Workers[0].Processes != 2 {
		t.Fatalf("state = %+v, want the old count", st.Workers)
	}
	f.run.reset()
	if err := f.reconcile(t, queue(2, 512)); err != nil {
		t.Fatal(err)
	}
	if calls := f.run.systemctl(); !has(calls, surplusOf("q", 3)) {
		t.Errorf("calls %q; want @3..@10 stopped", calls)
	}
}

func TestBadUnitRestoresPrevious(t *testing.T) {
	f := newFixture(t)
	if err := f.reconcile(t, queue(1, 512)); err != nil {
		t.Fatal(err)
	}
	before := f.unit(t, "zimam-worker-shop_q@.service")
	f.run.reset()
	f.run.badVerify = true
	if err := f.reconcile(t, queue(1, 2048)); err == nil {
		t.Fatal("a failed verify passed")
	}
	if after := f.unit(t, "zimam-worker-shop_q@.service"); after != before {
		t.Errorf("the unit wasn't restored:\n%s", after)
	}
	// Only the surplus stop, which comes before the files, ran.
	if calls := f.run.systemctl(); !slices.Equal(calls, []string{surplusOf("q", 2)}) {
		t.Errorf("systemctl ran after a failed test: %q", calls)
	}
	st, err := sites.Load(f.d.Paths, "shop")
	if err != nil || len(st.Workers) != 1 || st.Workers[0].MemoryMB != 512 {
		t.Errorf("state = %+v, %v; want the previous worker", st.Workers, err)
	}
}

func TestReconcileRecordsStateWithoutCommand(t *testing.T) {
	f := newFixture(t)
	if err := f.reconcile(t, contract.WorkerSpec{ID: "h", Kind: contract.WorkerCustom, Processes: 2, MemoryMB: 256, Command: "php artisan horizon --secret-flag"}); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(f.d.Paths.SiteState("shop"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "secret-flag") || strings.Contains(string(raw), `"command"`) {
		t.Errorf("the state holds the command:\n%s", raw)
	}
	var st struct {
		Workers []map[string]any `json:"workers"`
	}
	if err := json.Unmarshal(raw, &st); err != nil || len(st.Workers) != 1 || st.Workers[0]["id"] != "h" || st.Workers[0]["processes"] != 2.0 {
		t.Errorf("workers = %v, %v", st.Workers, err)
	}
	// An empty list removes everything, units and target included.
	f.run.reset()
	if err := f.reconcile(t); err != nil {
		t.Fatal(err)
	}
	if ids, _ := systemd.WorkerIDs(f.d, "shop"); len(ids) != 0 {
		t.Errorf("units left: %v", ids)
	}
	if st, _ := sites.Load(f.d.Paths, "shop"); st.Workers != nil {
		t.Errorf("workers left in the state: %v", st.Workers)
	}
	if calls := f.run.systemctl(); !has(calls, "disable --now --no-block zimam-site-shop.target") || calls[len(calls)-1] != "daemon-reload" {
		t.Errorf("removing all: %q", calls)
	}
}

func TestRestartWorkersByName(t *testing.T) {
	f := newFixture(t)
	if err := RestartWorkers(context.Background(), f.d, f.st); err != nil || len(f.run.calls) != 0 {
		t.Fatalf("no workers: %v, calls %q", err, f.run.calls)
	}
	st := f.st
	st.Workers = []sites.WorkerState{{ID: "q", Kind: contract.WorkerLaravelQueue, Processes: 2}}
	f.run.props["zimam-worker-shop_q@2.service"] = map[string]string{"ActiveState": "failed"}
	if err := RestartWorkers(context.Background(), f.d, st); err != nil {
		t.Fatal(err)
	}
	want := []string{"reset-failed zimam-worker-shop_q@2.service", "restart --no-block zimam-worker-shop_q@1.service zimam-worker-shop_q@2.service"}
	if calls := f.run.systemctl(); !slices.Equal(calls, want) {
		t.Errorf("calls = %q, want %q", calls, want)
	}
}

func TestStatusParsesSystemctlShow(t *testing.T) {
	f := newFixture(t)
	st := f.st
	st.Workers = []sites.WorkerState{{ID: "w", Kind: contract.WorkerCustom, Processes: 1}, {ID: "q", Kind: contract.WorkerLaravelQueue, Processes: 2}}
	f.run.props["zimam-worker-shop_q@2.service"] = map[string]string{"ActiveState": "inactive", "SubState": "dead", "NRestarts": "4", "StateChangeTimestamp": ""}
	res, err := Status(context.Background(), f.d, st)
	if err != nil {
		t.Fatal(err)
	}
	want := contract.SiteWorkersStatusResult{Name: "shop", Workers: []contract.WorkerStatus{
		{ID: "q", Instances: []contract.WorkerInstanceStatus{
			{Instance: 1, Active: "active", Sub: "running", Since: "2025-11-18T10:00:00Z"},
			{Instance: 2, Active: "inactive", Sub: "dead", Restarts: 4},
		}},
		{ID: "w", Instances: []contract.WorkerInstanceStatus{{Instance: 1, Active: "active", Sub: "running", Since: "2025-11-18T10:00:00Z"}}},
	}}
	if g, w := mustJSON(t, res), mustJSON(t, want); g != w {
		t.Errorf("status = %s\nwant %s", g, w)
	}
	for _, c := range f.run.calls {
		if !slices.Contains(c, "--timestamp=unix") {
			t.Errorf("show without unix timestamps: %q", c)
		}
	}
	// No workers: no call, and an empty list.
	f.run.reset()
	if res, err := Status(context.Background(), f.d, f.st); err != nil || len(f.run.calls) != 0 || mustJSON(t, res) != `{"name":"shop","workers":[]}` {
		t.Errorf("no workers: %s, %v, %d calls", mustJSON(t, res), err, len(f.run.calls))
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// A crash loop sits in auto-restart after failed exits, and once the unit's
// start limit is spent systemd says failed: both are failing. A clean exit
// waiting to restart (queue:restart, --max-time) is not.
func TestCrashLoopingWorkerStops(t *testing.T) {
	for _, c := range []struct {
		props map[string]string
		want  bool
	}{
		{map[string]string{"ActiveState": "failed", "SubState": "failed", "Result": "start-limit-hit"}, true},
		{map[string]string{"ActiveState": "activating", "SubState": "auto-restart", "Result": "exit-code", "ExecMainStatus": "1"}, true},
		{map[string]string{"ActiveState": "activating", "SubState": "auto-restart", "Result": "signal", "ExecMainStatus": "9"}, true},
		{map[string]string{"ActiveState": "activating", "SubState": "auto-restart", "Result": "success", "ExecMainStatus": "0"}, false},
		{map[string]string{"ActiveState": "active", "SubState": "running"}, false},
	} {
		f := newFixture(t)
		st := f.st
		st.Workers = []sites.WorkerState{{ID: "q", Kind: contract.WorkerLaravelQueue, Processes: 1}}
		f.run.props["zimam-worker-shop_q@1.service"] = c.props
		res, err := Status(context.Background(), f.d, st)
		if err != nil {
			t.Fatal(err)
		}
		if got := res.Workers[0].Instances[0].Failing; got != c.want {
			t.Errorf("%v: failing = %t, want %t", c.props, got, c.want)
		}
	}
}

func TestRestartingWorkerWarns(t *testing.T) {
	f := newFixture(t)
	st := f.st
	st.Workers = []sites.WorkerState{{ID: "q", Kind: contract.WorkerLaravelQueue, Processes: 2}, {ID: "w", Kind: contract.WorkerCustom, Processes: 1}}
	if err := sites.Save(f.d.Paths, st); err != nil {
		t.Fatal(err)
	}
	f.run.props["zimam-worker-shop_q@1.service"] = map[string]string{"NRestarts": "3"}
	f.run.props["zimam-worker-shop_q@2.service"] = map[string]string{"ActiveState": "activating", "SubState": "auto-restart", "Result": "exit-code", "ExecMainStatus": "1", "NRestarts": "7"}
	got, err := Metrics(context.Background(), f.d)
	if err != nil {
		t.Fatal(err)
	}
	want := []contract.WorkerMetric{{Site: "shop", Worker: "q", Restarts: 10, Failing: true}, {Site: "shop", Worker: "w"}}
	if !slices.Equal(got, want) {
		t.Errorf("metrics = %+v, want %+v", got, want)
	}
}
