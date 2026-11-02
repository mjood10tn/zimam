package maintenance

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"zimam/agent/contract"
	"zimam/agent/internal/audit"
	"zimam/agent/internal/config"
	"zimam/agent/internal/paths"
	"zimam/agent/internal/php"
	"zimam/agent/internal/sysexec"
)

var testNow = time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)

const aptCheckPath = "/usr/lib/update-notifier/apt-check"

var aptUpdate = sysexec.Script{Path: "/usr/bin/apt-get", Args: []string{"-o", "DPkg::Lock::Timeout=300", "update"}}

// fakeJobs stands in for the job manager, whose Reconcile has its own tests.
type fakeJobs struct {
	reconciled int
	err        error
	calls      atomic.Int32
}

func (j *fakeJobs) Reconcile(context.Context) (int, error) {
	j.calls.Add(1)
	return j.reconciled, j.err
}

// env is a server after bootstrap, under a temporary root: no sury keyring
// yet, update counts checked ten minutes ago, and an HTTPS server that
// serves keyring as <php_repo_url>apt.gpg (an error while it's nil).
type env struct {
	t       *testing.T
	d       sysexec.Deps
	run     *sysexec.FakeRunner
	jobs    *fakeJobs
	mu      sync.Mutex
	keyring []byte
	keyHits atomic.Int32
}

// script is a scripted answer that may name paths under the env's root.
type script func(p paths.Paths) sysexec.Script

func fixed(s sysexec.Script) script { return func(paths.Paths) sysexec.Script { return s } }

// logrotateRun is maintenance's logrotate run, exiting with code.
func logrotateRun(code int) script {
	return func(p paths.Paths) sysexec.Script {
		s := sysexec.Script{Path: logrotate, Args: []string{"--state", p.LogrotateState(), p.LogrotateConf()}}
		s.Result.ExitCode = code
		if code != 0 {
			s.Result.Stderr = []byte("error: skipping")
		}
		return s
	}
}

// newEnv scripts logrotate, apt-check and apt-get update to succeed. The
// scripts given come first, so a failing one answers the first call.
func newEnv(t *testing.T, first ...script) *env {
	t.Helper()
	e := &env{t: t, jobs: &fakeJobs{}}
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/php/apt.gpg" {
			http.NotFound(w, r)
			return
		}
		e.keyHits.Add(1)
		e.mu.Lock()
		defer e.mu.Unlock()
		if e.keyring == nil {
			http.Error(w, "unavailable", http.StatusInternalServerError)
			return
		}
		w.Write(e.keyring)
	}))
	t.Cleanup(srv.Close)
	p := paths.Paths{Root: t.TempDir()}
	for _, dir := range []string{p.StateDir(), p.LocksDir(), p.LogDir(), p.ConfigDir()} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	cfg := config.New("panel.example.com", "8.4", "8.4")
	cfg.PHPRepoURL = srv.URL + "/php/"
	var scripts []sysexec.Script
	for _, s := range append(first,
		logrotateRun(0),
		fixed(sysexec.Script{Path: aptCheckPath, Result: sysexec.Result{Stderr: []byte("5;2")}}),
		fixed(aptUpdate),
	) {
		scripts = append(scripts, s(p))
	}
	e.run = sysexec.NewFakeRunner(scripts...)
	e.d = sysexec.Deps{Paths: p, Run: e.run, Config: cfg, HTTP: srv.Client(), Logf: t.Logf, Now: func() time.Time { return testNow }}
	writeJSON(t, p.UpdatesCache(), map[string]any{"total": 1, "security": 0, "checked_at": testNow.Add(-10 * time.Minute)})
	return e
}

func (e *env) serve(keyring []byte) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.keyring = keyring
}

func (e *env) maintain() error {
	return Run(context.Background(), e.d, e.jobs, audit.Writer{Paths: e.d.Paths})
}

// commandLines is each command run, as one line.
func (e *env) commandLines() []string {
	var lines []string
	for _, c := range e.run.Calls() {
		lines = append(lines, strings.Join(append([]string{c.Path}, c.Args...), " "))
	}
	return lines
}

// installAptCheck puts update-notifier-common's apt-check in place.
func (e *env) installAptCheck() {
	e.t.Helper()
	if err := os.MkdirAll(filepath.Dir(e.d.Paths.AptCheck()), 0o755); err != nil {
		e.t.Fatal(err)
	}
	if err := os.WriteFile(e.d.Paths.AptCheck(), nil, 0o755); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) ran(path string) bool {
	for _, c := range e.run.Calls() {
		if c.Path == path {
			return true
		}
	}
	return false
}

// installRepo sets the sury repository up as bootstrap does, so a refresh
// that downloads the same key writes the same bytes.
func (e *env) installRepo() {
	e.t.Helper()
	e.serve(suryKeyring(e.t))
	d := e.d
	d.Run = sysexec.NewFakeRunner(aptUpdate)
	if err := php.EnsureRepo(context.Background(), d); err != nil {
		e.t.Fatalf("setting the repository up: %v", err)
	}
	e.keyHits.Store(0)
}

func suryKeyring(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile("../php/testdata/sury-apt.gpg")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func writeJSON(t *testing.T, path string, v any) {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func readUpdates(t *testing.T, p paths.Paths) contract.UpdateCounts {
	t.Helper()
	var u contract.UpdateCounts
	if err := json.Unmarshal(readFile(t, p.UpdatesCache()), &u); err != nil {
		t.Fatal(err)
	}
	return u
}

// recordOnDisk is keyring-refresh.json as the test reads it.
type recordOnDisk struct {
	AttemptedAt time.Time `json:"attempted_at"`
	Error       string    `json:"error"`
}

func readRecord(t *testing.T, p paths.Paths) recordOnDisk {
	t.Helper()
	var r recordOnDisk
	if err := json.Unmarshal(readFile(t, p.KeyringRefresh()), &r); err != nil {
		t.Fatal(err)
	}
	return r
}

// auditEntries decodes every line of the audit log; none when it doesn't
// exist.
func auditEntries(t *testing.T, p paths.Paths) []map[string]any {
	t.Helper()
	b, err := os.ReadFile(p.AuditLog())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var entries []map[string]any
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatalf("audit line %q: %v", l, err)
		}
		entries = append(entries, m)
	}
	return entries
}

func TestMaintenanceReconciles(t *testing.T) {
	e := newEnv(t)
	e.jobs.reconciled = 2
	if err := e.maintain(); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if n := e.jobs.calls.Load(); n != 1 {
		t.Errorf("Reconcile ran %d times, want once", n)
	}
}

// A config change whose process died before its test leaves a record that
// no process holds (M1 review, M4). Maintenance puts the previous file back
// and audits what it restored.
func TestMaintenanceRestoresAnAbandonedConfigChange(t *testing.T) {
	e := newEnv(t)
	p := e.d.Paths
	conf := filepath.Join(p.ConfigDir(), "pool.conf")
	if err := os.WriteFile(conf, []byte("new, untested"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(conf+".zimam-prev", []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(p.ConfigTxDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	rec := `{"changes":[{"path":"` + conf + `","had_prev":true}]}`
	if err := os.WriteFile(filepath.Join(p.ConfigTxDir(), "0123456789abcdef.json"), []byte(rec), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := e.maintain(); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if b, _ := os.ReadFile(conf); string(b) != "old" {
		t.Errorf("pool.conf = %q, want the previous content", b)
	}
	entries := auditEntries(t, p)
	if len(entries) != 1 {
		t.Fatalf("audit entries %v, want one", entries)
	}
	params, _ := entries[0]["params"].(map[string]any)
	if got, _ := params["restored_configs"].([]any); len(got) != 1 || got[0] != conf {
		t.Errorf("params %v, want restored_configs [%s]", params, conf)
	}
}

// Zimam's own rules rotate with Zimam's own state file, never Ubuntu's
// (§11.3).
func TestMaintenanceRunsLogrotateWithZimamState(t *testing.T) {
	e := newEnv(t)
	if err := e.maintain(); err != nil {
		t.Fatalf("Run: %v", err)
	}
	want := logrotate + " --state " + e.d.Paths.LogrotateState() + " " + e.d.Paths.LogrotateConf()
	if got := e.commandLines(); !slices.Equal(got, []string{want}) {
		t.Errorf("ran %q, want only %q", got, want)
	}
}

// A failed step doesn't stop the others; the run still fails.
func TestMaintenanceKeepsGoingAfterAFailedStep(t *testing.T) {
	e := newEnv(t)
	e.jobs.err = errors.New("systemctl show failed")
	err := e.maintain()
	if err == nil || !strings.Contains(err.Error(), "systemctl show failed") {
		t.Errorf("Run = %v, want the reconcile error", err)
	}
	if !e.ran(logrotate) {
		t.Error("logrotate didn't run after reconciling failed")
	}
}

func TestRefreshOnlyHourly(t *testing.T) {
	for _, tc := range []struct {
		name    string
		checked *time.Time // nil: no updates.json
		refresh bool
	}{
		{"30 minutes old", new(testNow.Add(-30 * time.Minute)), false},
		{"exactly an hour old", new(testNow.Add(-time.Hour)), false},
		{"61 minutes old", new(testNow.Add(-61 * time.Minute)), true},
		{"never checked", nil, true},
		{"from the future", new(testNow.Add(time.Hour)), true},
	} {
		e := newEnv(t)
		e.installAptCheck()
		os.Remove(e.d.Paths.UpdatesCache())
		if tc.checked != nil {
			writeJSON(t, e.d.Paths.UpdatesCache(), map[string]any{"total": 1, "security": 0, "checked_at": *tc.checked})
		}
		if err := e.maintain(); err != nil {
			t.Fatalf("%s: Run: %v", tc.name, err)
		}
		if got := e.ran(aptCheckPath); got != tc.refresh {
			t.Errorf("%s: apt-check ran = %v, want %v", tc.name, got, tc.refresh)
		}
		u := readUpdates(t, e.d.Paths)
		switch {
		case tc.refresh && (u.Total == nil || *u.Total != 5 || u.Security == nil || *u.Security != 2 || u.CheckedAt == nil || !u.CheckedAt.Equal(testNow)):
			t.Errorf("%s: updates.json = %+v, want 5 and 2, checked now", tc.name, u)
		case !tc.refresh && (u.Total == nil || *u.Total != 1 || !u.CheckedAt.Equal(*tc.checked)):
			t.Errorf("%s: updates.json = %+v, want it unchanged", tc.name, u)
		}
		if fi, err := os.Stat(e.d.Paths.UpdatesCache()); err != nil || fi.Mode().Perm() != 0o600 {
			t.Errorf("%s: updates.json mode: %v, %v; want 0600", tc.name, fi.Mode().Perm(), err)
		}
	}
}

// Without update-notifier-common, the counts come from a simulated
// dist-upgrade.
func TestRefreshWithoutAptCheck(t *testing.T) {
	out, err := os.ReadFile("testdata/apt-simulate.txt")
	if err != nil {
		t.Fatal(err)
	}
	e := newEnv(t, fixed(sysexec.Script{Path: "/usr/bin/apt-get", Args: []string{"-s", "dist-upgrade"}, Result: sysexec.Result{Stdout: out}}))
	os.Remove(e.d.Paths.UpdatesCache())
	if err := e.maintain(); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if e.ran(aptCheckPath) {
		t.Error("ran apt-check, which isn't installed")
	}
	if u := readUpdates(t, e.d.Paths); u.Total == nil || *u.Total != 5 || u.Security == nil || *u.Security != 2 {
		t.Errorf("updates.json = %+v, want 5 and 2", u)
	}
}

// A failed count keeps the last good one and its time, so the next run
// tries again; it never falls back to apt-get -s.
func TestRefreshFailureKeepsCounts(t *testing.T) {
	for name, s := range map[string]sysexec.Script{
		"apt-check fails":       {Path: aptCheckPath, Result: sysexec.Result{ExitCode: 1, Stderr: []byte("E: Error: BrokenCount > 0")}},
		"apt-check prints junk": {Path: aptCheckPath, Result: sysexec.Result{Stderr: []byte("Traceback (most recent call last):")}},
	} {
		e := newEnv(t, fixed(s))
		e.installAptCheck()
		checked := testNow.Add(-2 * time.Hour)
		writeJSON(t, e.d.Paths.UpdatesCache(), map[string]any{"total": 1, "security": 0, "checked_at": checked})
		if err := e.maintain(); err == nil {
			t.Errorf("%s: Run succeeded", name)
		}
		if u := readUpdates(t, e.d.Paths); u.Total == nil || *u.Total != 1 || !u.CheckedAt.Equal(checked) {
			t.Errorf("%s: updates.json = %+v, want the last good counts", name, u)
		}
		if e.ran("/usr/bin/apt-get") {
			t.Errorf("%s: fell back to apt-get: %q", name, e.commandLines())
		}
	}
}

func TestKeyringRefreshWeekly(t *testing.T) {
	for _, tc := range []struct {
		name    string
		keyring bool
		record  any // written to keyring-refresh.json; nil: no file
		refresh bool
	}{
		{"before the repository is set up", false, nil, false},
		{"never tried", true, nil, true},
		{"tried 6 days ago", true, map[string]any{"attempted_at": testNow.AddDate(0, 0, -6)}, false},
		{"tried 8 days ago", true, map[string]any{"attempted_at": testNow.AddDate(0, 0, -8)}, true},
		{"tried in the future", true, map[string]any{"attempted_at": testNow.AddDate(0, 0, 1)}, true},
		{"unreadable record", true, "not a record", true},
	} {
		e := newEnv(t)
		if tc.keyring {
			e.installRepo()
		} else {
			e.serve(suryKeyring(t))
		}
		if tc.record != nil {
			writeJSON(t, e.d.Paths.KeyringRefresh(), tc.record)
		}
		before, _ := os.ReadFile(e.d.Paths.KeyringRefresh())
		if err := e.maintain(); err != nil {
			t.Fatalf("%s: Run: %v", tc.name, err)
		}
		hits := e.keyHits.Load()
		if (hits > 0) != tc.refresh || hits > 1 {
			t.Errorf("%s: downloaded apt.gpg %d times, want refresh = %v", tc.name, hits, tc.refresh)
		}
		if got := e.ran("/usr/bin/apt-get"); got != tc.refresh {
			t.Errorf("%s: apt-get update ran = %v, want %v", tc.name, got, tc.refresh)
		}
		after, err := os.ReadFile(e.d.Paths.KeyringRefresh())
		switch {
		case !tc.keyring && !errors.Is(err, os.ErrNotExist):
			t.Errorf("%s: wrote a refresh record before the repository exists (%v)", tc.name, err)
		case tc.refresh:
			if r := readRecord(t, e.d.Paths); !r.AttemptedAt.Equal(testNow) || r.Error != "" {
				t.Errorf("%s: record = %+v, want attempted now, no error", tc.name, r)
			}
			if fi, err := os.Stat(e.d.Paths.KeyringRefresh()); err != nil || fi.Mode().Perm() != 0o600 {
				t.Errorf("%s: record mode: %v, %v; want 0600", tc.name, fi.Mode().Perm(), err)
			}
		case !bytes.Equal(after, before):
			t.Errorf("%s: the record changed without a refresh:\n%s", tc.name, after)
		}
	}
}

// A failed refresh keeps the installed keyring, records the attempt, and
// doesn't fail the run: the key keeps working, and next week tries again.
func TestKeyringRefreshFailureKeepsRunning(t *testing.T) {
	other := bytes.Clone(suryKeyring(t))
	other[20] ^= 0xff // still a well-formed key, with another fingerprint
	for name, served := range map[string][]byte{
		"download error": nil,
		"wrong key":      other,
	} {
		e := newEnv(t)
		e.installRepo()
		keyring := readFile(t, e.d.Paths.SuryKeyring())
		e.serve(served)
		if err := e.maintain(); err != nil {
			t.Errorf("%s: Run = %v, want nil", name, err)
		}
		if e.keyHits.Load() != 1 {
			t.Errorf("%s: downloaded apt.gpg %d times, want once", name, e.keyHits.Load())
		}
		if got := readFile(t, e.d.Paths.SuryKeyring()); !bytes.Equal(got, keyring) {
			t.Errorf("%s: the installed keyring changed", name)
		}
		if r := readRecord(t, e.d.Paths); !r.AttemptedAt.Equal(testNow) || r.Error == "" {
			t.Errorf("%s: record = %+v, want attempted now with the error", name, r)
		}
	}
}

func TestMaintenanceAuditOnlyOnChange(t *testing.T) {
	t.Run("nothing changed", func(t *testing.T) {
		e := newEnv(t)
		e.installRepo()
		writeJSON(t, e.d.Paths.KeyringRefresh(), map[string]any{"attempted_at": testNow.AddDate(0, 0, -1)})
		if err := e.maintain(); err != nil {
			t.Fatalf("Run: %v", err)
		}
		if entries := auditEntries(t, e.d.Paths); len(entries) != 0 {
			t.Errorf("audited %v", entries)
		}
	})
	t.Run("a refresh that rewrote the same keyring", func(t *testing.T) {
		e := newEnv(t)
		e.installRepo()
		if err := e.maintain(); err != nil {
			t.Fatalf("Run: %v", err)
		}
		if e.keyHits.Load() != 1 {
			t.Fatal("the keyring wasn't refreshed")
		}
		if entries := auditEntries(t, e.d.Paths); len(entries) != 0 {
			t.Errorf("audited %v", entries)
		}
	})
	t.Run("jobs reconciled", func(t *testing.T) {
		e := newEnv(t)
		e.jobs.reconciled = 1
		if err := e.maintain(); err != nil {
			t.Fatalf("Run: %v", err)
		}
		checkEntry(t, e, "ok", map[string]any{"reconciled_jobs": 1.0, "keyring_refreshed": false})
	})
	t.Run("keyring changed", func(t *testing.T) {
		e := newEnv(t)
		e.installRepo()
		if err := os.WriteFile(e.d.Paths.SuryKeyring(), []byte("the key before sury extended it"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := e.maintain(); err != nil {
			t.Fatalf("Run: %v", err)
		}
		checkEntry(t, e, "ok", map[string]any{"reconciled_jobs": 0.0, "keyring_refreshed": true})
	})
	t.Run("a change and a failed step", func(t *testing.T) {
		e := newEnv(t, logrotateRun(1))
		e.jobs.reconciled = 1
		if err := e.maintain(); err == nil {
			t.Fatal("Run succeeded with logrotate failing")
		}
		checkEntry(t, e, "error:internal", map[string]any{"reconciled_jobs": 1.0, "keyring_refreshed": false})
	})
}

// checkEntry checks the run's one audit entry: cli:root's maintenance, with
// what changed in params.
func checkEntry(t *testing.T, e *env, outcome string, params map[string]any) {
	t.Helper()
	entries := auditEntries(t, e.d.Paths)
	if len(entries) != 1 {
		t.Fatalf("audit log has %d entries, want 1: %v", len(entries), entries)
	}
	got := entries[0]
	if got["actor"] != "cli:root" || got["command"] != "maintenance" || got["outcome"] != outcome || got["peer_uid"] != nil {
		t.Errorf("entry = %v, want cli:root's maintenance with outcome %s", got, outcome)
	}
	if p, _ := got["params"].(map[string]any); !mapsEqual(p, params) {
		t.Errorf("params = %v, want %v", got["params"], params)
	}
	if ts, _ := got["ts"].(string); ts != testNow.Format(time.RFC3339) {
		t.Errorf("ts = %v, want %v", got["ts"], testNow.Format(time.RFC3339))
	}
}

func mapsEqual(a, b map[string]any) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

// Two runs never overlap: the timer's and one started by hand, for example.
func TestMaintenanceWaitsForItsLock(t *testing.T) {
	e := newEnv(t)
	unlock, err := sysexec.Lock(context.Background(), e.d.Paths.MaintenanceLock())
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	defer func(w time.Duration) { lockWait = w }(lockWait)
	lockWait = 100 * time.Millisecond
	if err := e.maintain(); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Run = %v, want it to give up waiting for the lock", err)
	}
	if calls := e.commandLines(); len(calls) != 0 || e.jobs.calls.Load() != 0 {
		t.Errorf("ran %q and reconciled %d times while another run held the lock", calls, e.jobs.calls.Load())
	}
}
