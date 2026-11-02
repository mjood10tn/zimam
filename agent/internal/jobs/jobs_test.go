package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"zimam/agent/contract"
	"zimam/agent/internal/audit"
	"zimam/agent/internal/helper"
	"zimam/agent/internal/paths"
)

const testRequestID = "01JBX7Q2M5YQ8T3N6V0R4K9W1E"

var testUID = uint32(1001)

// callerCtx is a request's context, as the server builds it.
var callerCtx = audit.WithCaller(context.Background(), audit.Caller{PeerUID: &testUID, Actor: "user:1", RequestID: testRequestID})

var testStart = time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)

type testParams struct {
	Name     contract.SiteName `json:"name"`
	Password string            `json:"password,omitempty" secret:"true"`
}

func (testParams) Validate() error { return nil }

type testResult struct {
	Greeting string `json:"greeting"`
}

// The test job commands use real names, for their §5.7 limits, with test
// params. The manager finds them through its lookup and they're never
// registered, so the real commands' registrations can't collide with them.
var testCommands = map[string]contract.Descriptor{
	"php.default": {
		Name: "php.default", Kind: contract.KindJob, Audited: true,
		Params:  func() contract.Params { return &testParams{} },
		Result:  testResult{},
		Subject: func(contract.Params) string { return "server" },
	},
	"site.provision": {
		Name: "site.provision", Kind: contract.KindJob, Audited: true,
		Params:  func() contract.Params { return &testParams{} },
		Result:  testResult{},
		Subject: func(p contract.Params) string { return "site:" + string(p.(*testParams).Name) },
	},
}

type startCall struct {
	unit        string
	props, argv []string
}

// fakeSystemd stands in for systemd. A started unit stays active until the
// test changes it; a unit it doesn't know is not-found, as systemctl show
// reports a unit that's gone.
type fakeSystemd struct {
	mu       sync.Mutex
	units    map[string]UnitState
	started  []startCall
	stopped  []string
	reset    []string
	startErr error
	onStop   func(unit string) // runs inside Stop, which waits for the job's process as systemctl stop does
}

func (f *fakeSystemd) StartTransient(_ context.Context, unit string, props, argv []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.started = append(f.started, startCall{unit, slices.Clone(props), slices.Clone(argv)})
	if f.startErr != nil {
		return f.startErr
	}
	f.units[unit] = UnitState{LoadState: "loaded", ActiveState: "active", Result: "success"}
	return nil
}

func (f *fakeSystemd) Stop(_ context.Context, unit string) error {
	f.mu.Lock()
	f.stopped = append(f.stopped, unit)
	onStop := f.onStop
	f.mu.Unlock()
	if onStop != nil {
		onStop(unit)
	}
	f.setUnit(unit, UnitState{}) // a stopped transient unit is garbage-collected
	return nil
}

func (f *fakeSystemd) Show(_ context.Context, unit string) (UnitState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if u, ok := f.units[unit]; ok {
		return u, nil
	}
	return UnitState{LoadState: "not-found", ActiveState: "inactive", Result: "success"}, nil
}

func (f *fakeSystemd) ResetFailed(_ context.Context, unit string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reset = append(f.reset, unit)
	return nil
}

// setUnit sets a unit's state; the zero UnitState removes the unit.
func (f *fakeSystemd) setUnit(unit string, u UnitState) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if u == (UnitState{}) {
		delete(f.units, unit)
		return
	}
	f.units[unit] = u
}

type recordAudit struct {
	mu      sync.Mutex
	entries []audit.Entry
	err     error // when set, Write records nothing and fails
}

func (r *recordAudit) Write(e audit.Entry) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return r.err
	}
	r.entries = append(r.entries, e)
	return nil
}

func (r *recordAudit) all() []audit.Entry {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.entries)
}

func (r *recordAudit) last(t *testing.T) audit.Entry {
	t.Helper()
	all := r.all()
	if len(all) == 0 {
		t.Fatal("no audit entries")
	}
	return all[len(all)-1]
}

type fixture struct {
	m     *Manager
	sd    *fakeSystemd
	audit *recordAudit
	mu    sync.Mutex
	now   time.Time
}

// newFixture returns a manager under a temporary root, with the two test
// job commands: php.default logs three lines and succeeds; site.provision
// fails for the names "taken" (conflict) and "broken" (a plain error).
func newFixture(t *testing.T) *fixture {
	t.Helper()
	p := paths.Paths{Root: t.TempDir()}
	for _, dir := range []string{p.LocksDir(), p.JobsDir(), p.JobLogsDir()} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	f := &fixture{sd: &fakeSystemd{units: map[string]UnitState{}}, audit: &recordAudit{}, now: testStart}
	f.m = &Manager{
		Paths:   p,
		Systemd: f.sd,
		Audit:   f.audit,
		Handlers: map[string]Handler{
			"php.default": func(_ context.Context, j *Context, _ contract.Params) (any, error) {
				j.Logf("hello %s", "world")
				j.Logf("two\nlines")
				return testResult{Greeting: "hi"}, nil
			},
			"site.provision": func(_ context.Context, _ *Context, p contract.Params) (any, error) {
				switch p.(*testParams).Name {
				case "taken":
					return nil, &contract.AgentError{Code: contract.ErrConflict, Message: "the site exists"}
				case "broken":
					return nil, errors.New("disk on fire")
				}
				return testResult{Greeting: "built"}, nil
			},
		},
		LogGID: -1,
		Now:    f.clock,
		lookup: func(name string) (contract.Descriptor, bool) {
			d, ok := testCommands[name]
			return d, ok
		},
	}
	return f
}

func (f *fixture) clock() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

func (f *fixture) advance(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now = f.now.Add(d)
}

func (f *fixture) submit(t *testing.T, name string, p *testParams) contract.JobID {
	t.Helper()
	id, err := f.m.Submit(callerCtx, testCommands[name], p)
	if err != nil {
		t.Fatalf("Submit(%s): %v", name, err)
	}
	return id
}

func (f *fixture) run(t *testing.T, id contract.JobID) {
	t.Helper()
	if err := f.m.RunJob(context.Background(), id); err != nil {
		t.Fatalf("RunJob: %v", err)
	}
}

// state reads the job's state.json, without reconciling.
func (f *fixture) state(t *testing.T, id contract.JobID) state {
	t.Helper()
	st, err := f.m.readState(id)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

// setStatus moves a job to s, as the runner would.
func (f *fixture) setStatus(t *testing.T, id contract.JobID, s contract.JobState) {
	t.Helper()
	st := f.state(t, id)
	st.Status = s
	if err := f.m.writeState(id, st); err != nil {
		t.Fatal(err)
	}
}

func expectCode(t *testing.T, err error, code contract.ErrorCode) {
	t.Helper()
	var ae *contract.AgentError
	if !errors.As(err, &ae) || ae.Code != code {
		t.Fatalf("err = %v, want %s", err, code)
	}
}

func checkFile(t *testing.T, path string, mode fs.FileMode) {
	t.Helper()
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode() != mode {
		t.Errorf("%s: mode %v, want %v", path, fi.Mode(), mode)
	}
	if uid := fi.Sys().(*syscall.Stat_t).Uid; uid != uint32(os.Getuid()) {
		t.Errorf("%s: owner %d, want %d", path, uid, os.Getuid())
	}
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func TestSubmitWritesSpecAndState(t *testing.T) {
	f := newFixture(t)
	id := f.submit(t, "site.provision", &testParams{Name: "shop", Password: "hunter2"})
	p := f.m.Paths
	checkFile(t, p.JobDir(id), fs.ModeDir|0o700)
	checkFile(t, p.JobSpec(id), 0o600)
	checkFile(t, p.JobState(id), 0o600)

	sp, err := f.m.readSpec(id)
	if err != nil {
		t.Fatal(err)
	}
	if sp.Command != "site.provision" || sp.Subject != "site:shop" || !strings.Contains(string(sp.Params), "hunter2") || sp.Caller.Actor != "user:1" {
		t.Errorf("spec = %+v; want the full params, secrets included, and the caller", sp)
	}
	st := f.state(t, id)
	if st.Job != id || st.Status != contract.JobQueued || st.Command != "site.provision" || st.Subject != "site:shop" || !st.QueuedAt.Equal(testStart) {
		t.Errorf("state = %+v, want a queued site.provision for site:shop", st)
	}
}

func TestSubmitBusySameSubject(t *testing.T) {
	f := newFixture(t)
	f.submit(t, "site.provision", &testParams{Name: "shop"})
	_, err := f.m.Submit(callerCtx, testCommands["site.provision"], &testParams{Name: "shop"})
	expectCode(t, err, contract.ErrBusy)
	f.submit(t, "site.provision", &testParams{Name: "blog"}) // another subject is free
}

func TestSubmitBusyGlobalCap(t *testing.T) {
	f := newFixture(t)
	first := f.submit(t, "site.provision", &testParams{Name: "shop"})
	f.submit(t, "site.provision", &testParams{Name: "blog"})
	_, err := f.m.Submit(callerCtx, testCommands["php.default"], &testParams{})
	expectCode(t, err, contract.ErrBusy)
	f.setStatus(t, first, contract.JobSucceeded) // a finished job frees its place
	f.submit(t, "php.default", &testParams{})
}

func TestSubmitArgvHasNoSecrets(t *testing.T) {
	f := newFixture(t)
	id := f.submit(t, "site.provision", &testParams{Name: "shop", Password: "hunter2"})
	if len(f.sd.started) != 1 {
		t.Fatalf("started %d units, want 1", len(f.sd.started))
	}
	c := f.sd.started[0]
	wantArgv := []string{f.m.Paths.AgentBinary(), "job", "run", string(id)}
	wantProps := []string{"Type=exec", "MemoryMax=512M", "MemorySwapMax=0", "RuntimeMaxSec=300"}
	if c.unit != "zimam-job-"+string(id) || !slices.Equal(c.argv, wantArgv) || !slices.Equal(c.props, wantProps) {
		t.Errorf("started %+v, want unit zimam-job-%s, props %q, argv %q", c, id, wantProps, wantArgv)
	}
	if all := strings.Join(append(append([]string{c.unit}, c.props...), c.argv...), " "); strings.Contains(all, "hunter2") {
		t.Errorf("the unit's command line holds the password: %s", all)
	}
}

func TestSubmitAudited(t *testing.T) {
	f := newFixture(t)
	id := f.submit(t, "site.provision", &testParams{Name: "shop", Password: "hunter2"})
	e := f.audit.last(t)
	if e.Outcome != "accepted" || e.Job != id || e.Command != "site.provision" || e.Actor != "user:1" ||
		e.RequestID != testRequestID || e.PeerUID == nil || *e.PeerUID != testUID {
		t.Errorf("entry = %+v, want accepted with the caller and the job", e)
	}
	b, _ := json.Marshal(e.Params)
	if !strings.Contains(string(b), `"password":"[redacted]"`) || strings.Contains(string(b), "hunter2") {
		t.Errorf("audited params %s, want the password redacted", b)
	}
	// A refused submission is audited too.
	_, err := f.m.Submit(callerCtx, testCommands["site.provision"], &testParams{Name: "shop"})
	expectCode(t, err, contract.ErrBusy)
	if all := f.audit.all(); len(all) != 2 || all[1].Outcome != "error:busy" {
		t.Errorf("audit = %+v, want accepted then error:busy", all)
	}
}

// A precheck's refusal reaches the caller as it is, is audited, and leaves no
// job behind.
func TestSubmitPrecheck(t *testing.T) {
	f := newFixture(t)
	f.m.Prechecks = map[string]Precheck{
		"site.provision": func(_ context.Context, p contract.Params) error {
			if p.(*testParams).Name == "taken" {
				return &contract.AgentError{Code: contract.ErrConflict, Message: "the site exists"}
			}
			return nil
		},
	}
	_, err := f.m.Submit(callerCtx, testCommands["site.provision"], &testParams{Name: "taken"})
	expectCode(t, err, contract.ErrConflict)
	if ids, err := f.m.jobIDs(); len(ids) != 0 || err != nil {
		t.Errorf("the refused submission left jobs %v (%v)", ids, err)
	}
	if len(f.sd.started) != 0 {
		t.Errorf("started %d units for a refused submission", len(f.sd.started))
	}
	if e := f.audit.last(t); e.Outcome != "error:conflict" || e.Command != "site.provision" {
		t.Errorf("entry = %+v, want site.provision error:conflict", e)
	}
	f.submit(t, "site.provision", &testParams{Name: "shop"}) // the precheck lets others through
}

// §5.8: a change that can't be audited doesn't happen. When the accepted
// entry can't be written, no job is created and no unit starts.
func TestSubmitRefusedWhenAuditFails(t *testing.T) {
	f := newFixture(t)
	f.audit.err = errors.New("no space left on device")
	_, err := f.m.Submit(callerCtx, testCommands["php.default"], &testParams{})
	expectCode(t, err, contract.ErrInternal)
	if len(f.sd.started) != 0 {
		t.Errorf("started %d units, want none", len(f.sd.started))
	}
	if ids, _ := f.m.jobIDs(); len(ids) != 0 {
		t.Errorf("created jobs %v, want none", ids)
	}
	// Once the log can be written again, jobs run as before.
	f.audit.err = nil
	id := f.submit(t, "php.default", &testParams{})
	if e := f.audit.last(t); e.Outcome != "accepted" || e.Job != id {
		t.Errorf("entry = %+v, want accepted", e)
	}
}

func TestSubmitStartFailure(t *testing.T) {
	f := newFixture(t)
	f.sd.startErr = errors.New("systemd-run failed")
	id, err := f.m.Submit(callerCtx, testCommands["php.default"], &testParams{})
	if err == nil {
		t.Fatal("Submit succeeded without a unit")
	}
	st := f.state(t, id)
	if st.Status != contract.JobFailed || exists(f.m.Paths.JobSpec(id)) {
		t.Errorf("state %s, spec kept %t; want failed and spec.json gone", st.Status, exists(f.m.Paths.JobSpec(id)))
	}
	if e := f.audit.last(t); e.Outcome != "error:internal" || e.Job != id {
		t.Errorf("entry = %+v, want error:internal for the job", e)
	}
}

func TestRunSuccess(t *testing.T) {
	f := newFixture(t)
	id := f.submit(t, "php.default", &testParams{})
	f.run(t, id)
	res, err := f.m.Status(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	logSize := int64(len(f.logContent(t, id)))
	if res.Status != contract.JobSucceeded || res.ErrorCode != nil || string(res.Result) != `{"greeting":"hi"}` ||
		res.StartedAt == nil || res.FinishedAt == nil || res.LogSize != logSize {
		t.Errorf("status = %+v, want succeeded with the result and log_size %d", res, logSize)
	}
	if e := f.audit.last(t); e.Outcome != "ok" || e.Job != id || e.Command != "php.default" || e.Actor != "user:1" {
		t.Errorf("entry = %+v, want ok for the job, with the submitter", e)
	}
}

func TestRunFailureRecordsCodeAndMessage(t *testing.T) {
	f := newFixture(t)
	for _, tc := range []struct {
		name    contract.SiteName
		code    contract.ErrorCode
		message string
	}{
		{name: "taken", code: contract.ErrConflict, message: "the site exists"},
		{name: "broken", code: contract.ErrInternal, message: "disk on fire"},
	} {
		id := f.submit(t, "site.provision", &testParams{Name: tc.name})
		f.run(t, id)
		st := f.state(t, id)
		if st.Status != contract.JobFailed || st.ErrorCode == nil || *st.ErrorCode != tc.code || st.Message != tc.message {
			t.Errorf("%s: state = %+v, want failed with %s %q", tc.name, st, tc.code, tc.message)
		}
		if e := f.audit.last(t); e.Outcome != "error:"+string(tc.code) {
			t.Errorf("%s: entry outcome %s, want error:%s", tc.name, e.Outcome, tc.code)
		}
	}
}

func TestRunDeletesSpec(t *testing.T) {
	f := newFixture(t)
	id := f.submit(t, "php.default", &testParams{})
	f.run(t, id)
	if exists(f.m.Paths.JobSpec(id)) {
		t.Error("spec.json, with its secrets, outlived the job")
	}
}

func (f *fixture) logContent(t *testing.T, id contract.JobID) string {
	t.Helper()
	b, err := os.ReadFile(f.m.Paths.JobLog(id))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestRunLogLineFormat(t *testing.T) {
	f := newFixture(t)
	id := f.submit(t, "php.default", &testParams{})
	f.run(t, id)
	want := "[2026-10-28T12:00:00Z] hello world\n[2026-10-28T12:00:00Z] two\n[2026-10-28T12:00:00Z] lines\n"
	if got := f.logContent(t, id); got != want {
		t.Errorf("log =\n%s\nwant\n%s", got, want)
	}
}

func TestJobLogMode0640(t *testing.T) {
	f := newFixture(t)
	id := f.submit(t, "php.default", &testParams{})
	f.run(t, id)
	checkFile(t, f.m.Paths.JobLog(id), 0o640)
}

func TestStatusReconcilesFirst(t *testing.T) {
	f := newFixture(t)
	id := f.submit(t, "php.default", &testParams{})
	f.setStatus(t, id, contract.JobRunning)
	f.sd.setUnit(unitName(id), UnitState{LoadState: "loaded", ActiveState: "failed", Result: "oom-kill"})
	res, err := f.m.Status(context.Background(), id)
	if err != nil || res.Status != contract.JobFailed || res.Message != "oom-kill" {
		t.Errorf("Status = %+v, %v; want failed with oom-kill", res, err)
	}
	_, err = f.m.Status(context.Background(), "01JBX7Q3A1B2C3D4E5F6G7H8J9")
	expectCode(t, err, contract.ErrNotFound)
}

func TestRunGivesHandlersTheHelper(t *testing.T) {
	f := newFixture(t)
	f.m.Helper = &helper.Client{AgentBinary: "/usr/local/sbin/zimam-agent"}
	var got *helper.Client
	f.m.Handlers["php.default"] = func(_ context.Context, j *Context, _ contract.Params) (any, error) {
		got = j.Helper
		return testResult{Greeting: "hi"}, nil
	}
	f.run(t, f.submit(t, "php.default", &testParams{}))
	if got != f.m.Helper {
		t.Errorf("the handler got helper %v, want the manager's %v", got, f.m.Helper)
	}
}

func TestNewJobIDIsULID(t *testing.T) {
	prev := newJobID(testStart)
	for i := 1; i < 1000; i++ {
		id := newJobID(testStart.Add(time.Duration(i) * time.Millisecond))
		if err := id.Validate(); err != nil {
			t.Fatalf("%s: %v", id, err)
		}
		if id <= prev {
			t.Fatalf("%s doesn't sort after %s, a millisecond earlier", id, prev)
		}
		prev = id
	}
}
