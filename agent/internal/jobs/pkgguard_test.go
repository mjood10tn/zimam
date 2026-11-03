package jobs

import (
	"context"
	"errors"
	"testing"
	"time"

	"zimam/agent/contract"
	"zimam/agent/internal/sysexec"
)

// holdRunner reports, for each command, whether the job's cancel lock was
// held while it ran.
type holdRunner struct {
	lock string
	held map[string]bool
}

func (r *holdRunner) Run(_ context.Context, c sysexec.Cmd) (sysexec.Result, error) {
	unlock, ok, err := sysexec.TryLock(r.lock)
	if err != nil {
		return sysexec.Result{}, err
	}
	if ok {
		unlock()
	}
	r.held[c.Path+" "+c.Args[len(c.Args)-1]] = !ok
	return sysexec.Result{}, nil
}

// apt-get and dpkg run under the job's cancel lock, so job.cancel can't stop
// dpkg midway; apt-get update and everything else stay cancellable.
func TestPackageCommandsHoldTheCancelLock(t *testing.T) {
	f := newFixture(t)
	id := f.submit(t, "php.default", &testParams{})
	r := &holdRunner{lock: f.m.Paths.JobCancelLock(id), held: map[string]bool{}}
	f.m.Run = r
	f.m.Handlers["php.default"] = func(ctx context.Context, j *Context, _ contract.Params) (any, error) {
		for _, c := range []sysexec.Cmd{
			{Path: "/usr/bin/apt-get", Args: []string{"-o", "DPkg::Lock::Timeout=300", "update"}},
			{Path: "/usr/bin/apt-get", Args: []string{"-o", "DPkg::Lock::Timeout=300", "install", "-y", "php8.4-fpm"}},
			{Path: "/usr/bin/apt-get", Args: []string{"-o", "DPkg::Lock::Timeout=300", "purge", "-y", "php8.4-fpm"}},
			{Path: "/usr/bin/dpkg", Args: []string{"--configure", "-a"}},
			{Path: "/usr/bin/systemctl", Args: []string{"disable", "--now", "php8.4-fpm"}},
		} {
			if _, err := j.Run.Run(ctx, c); err != nil {
				return nil, err
			}
		}
		return testResult{}, nil
	}
	f.run(t, id)
	if st := f.state(t, id); st.Status != contract.JobSucceeded {
		t.Fatalf("state = %+v, want succeeded", st)
	}
	want := map[string]bool{
		"/usr/bin/apt-get update":       false,
		"/usr/bin/apt-get php8.4-fpm":   true, // install and purge share a key: both must hold it
		"/usr/bin/dpkg -a":              true,
		"/usr/bin/systemctl php8.4-fpm": false,
	}
	for cmd, held := range want {
		if got, ok := r.held[cmd]; !ok || got != held {
			t.Errorf("%s ran with the cancel lock held = %v (ran: %v), want %v", cmd, got, ok, held)
		}
	}
}

// blockRunner holds every command until released.
type blockRunner struct {
	entered chan struct{}
	release chan struct{}
}

func (r *blockRunner) Run(ctx context.Context, _ sysexec.Cmd) (sysexec.Result, error) {
	close(r.entered)
	select {
	case <-r.release:
		return sysexec.Result{}, nil
	case <-ctx.Done():
		return sysexec.Result{}, ctx.Err()
	}
}

// While dpkg runs, job.cancel answers conflict and stops nothing; once the
// package step is over, the job can be cancelled.
func TestCancelRefusedWhilePackagesChange(t *testing.T) {
	f := newFixture(t)
	r := &blockRunner{entered: make(chan struct{}), release: make(chan struct{})}
	f.m.Run = r
	afterApt := make(chan struct{})
	f.m.Handlers["php.default"] = func(ctx context.Context, j *Context, _ contract.Params) (any, error) {
		if _, err := j.Run.Run(ctx, sysexec.Cmd{Path: "/usr/bin/apt-get", Args: []string{"install", "-y", "php8.4-fpm"}}); err != nil {
			return nil, err
		}
		close(afterApt)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	id := f.submit(t, "php.default", &testParams{})
	runCtx, sigterm := context.WithCancel(context.Background())
	runErr := make(chan error, 1)
	go func() { runErr <- f.m.RunJob(runCtx, id) }()
	<-r.entered

	_, err := f.m.Cancel(callerCtx, id)
	expectCode(t, err, contract.ErrConflict)
	var ae *contract.AgentError
	if errors.As(err, &ae) && ae.Message == finished(id, contract.JobRunning).(*contract.AgentError).Message {
		t.Errorf("message = %q, want one that says packages are changing", ae.Message)
	}
	if len(f.sd.stopped) != 0 {
		t.Errorf("stopped %q while dpkg ran", f.sd.stopped)
	}
	if e := f.audit.last(t); e.Command != "job.cancel" || e.Outcome != "error:conflict" {
		t.Errorf("entry = %+v, want job.cancel error:conflict", e)
	}

	close(r.release)
	<-afterApt
	f.sd.onStop = func(string) {
		sigterm()
		<-runErr
	}
	if res, err := f.m.Cancel(callerCtx, id); err != nil || res.Status != contract.JobCancelled {
		t.Fatalf("Cancel after the package step = %+v, %v; want cancelled", res, err)
	}
}

// A job whose package step waits for the cancel lock, because job.cancel
// holds it while stopping the unit, ends when its unit is stopped.
func TestPackageStepWaitingForCancelEnds(t *testing.T) {
	f := newFixture(t)
	f.m.Run = sysexec.NewFakeRunner(sysexec.Script{Path: "/usr/bin/dpkg", Args: []string{"--configure", "-a"}})
	f.m.Handlers["php.default"] = func(ctx context.Context, j *Context, _ contract.Params) (any, error) {
		_, err := j.Run.Run(ctx, sysexec.Cmd{Path: "/usr/bin/dpkg", Args: []string{"--configure", "-a"}})
		return nil, err
	}
	id := f.submit(t, "php.default", &testParams{})
	unlock, ok, err := sysexec.TryLock(f.m.Paths.JobCancelLock(id))
	if err != nil || !ok {
		t.Fatalf("TryLock = %v, %v", ok, err)
	}
	defer unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if err := f.m.RunJob(ctx, id); err != nil {
		t.Fatalf("RunJob = %v, want nil once stopped", err)
	}
	if st := f.state(t, id); st.Status != contract.JobRunning {
		t.Errorf("state = %s, want running, left for Cancel to settle", st.Status)
	}
}
