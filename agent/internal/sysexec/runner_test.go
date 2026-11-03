package sysexec

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestRunnerRequiresAbsolutePath(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "ran")
	for _, path := range []string{"touch", "./touch", "bin/touch", ""} {
		_, err := ExecRunner{}.Run(context.Background(), Cmd{Path: path, Args: []string{marker}})
		if err == nil {
			t.Errorf("Run(%q) = nil error, want a refusal", path)
		}
	}
	if _, err := os.Stat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Error("a relative path was run")
	}
}

func TestRunnerEnvIsMinimal(t *testing.T) {
	t.Setenv("ZIMAM_LEAK", "secret")
	res, err := ExecRunner{}.Run(context.Background(), Cmd{Path: "/usr/bin/env", Env: []string{"FOO=bar"}})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	got := strings.Split(strings.TrimSpace(string(res.Stdout)), "\n")
	slices.Sort(got)
	want := []string{"FOO=bar", "LANG=C.UTF-8", "PATH=/usr/sbin:/usr/bin:/sbin:/bin"}
	if !slices.Equal(got, want) {
		t.Errorf("environment = %q, want %q", got, want)
	}
}

func TestRunnerStdinAndOutput(t *testing.T) {
	res, err := ExecRunner{}.Run(context.Background(), Cmd{Path: "/usr/bin/cat", Stdin: strings.NewReader("s3cret")})
	if err != nil || string(res.Stdout) != "s3cret" || res.ExitCode != 0 {
		t.Errorf("cat: %q, %d, %v", res.Stdout, res.ExitCode, err)
	}
	dir := t.TempDir()
	res, err = ExecRunner{}.Run(context.Background(), Cmd{Path: "/usr/bin/pwd", Dir: dir})
	if err != nil || strings.TrimSpace(string(res.Stdout)) != dir {
		t.Errorf("pwd in %s: %q, %v", dir, res.Stdout, err)
	}
}

func TestRunnerNonZeroExit(t *testing.T) {
	res, err := ExecRunner{}.Run(context.Background(), Cmd{Path: "/usr/bin/ls", Args: []string{"/nonexistent-zimam"}})
	var exitErr *ExitError
	if !errors.As(err, &exitErr) || exitErr.Code != 2 || res.ExitCode != 2 {
		t.Fatalf("ls of a missing file: %v, exit %d; want ExitError with code 2", err, res.ExitCode)
	}
	if len(res.Stderr) == 0 || !strings.Contains(err.Error(), "/usr/bin/ls") {
		t.Errorf("stderr %q, error %q", res.Stderr, err)
	}
}

// A command's output is capped: stdout, which callers parse, fails the run
// when it passes the cap; stderr keeps its last bytes, where the reason is.
func TestRunnerCapsOutput(t *testing.T) {
	oldOut, oldErr := maxStdout, maxStderr
	maxStdout, maxStderr = 1000, 100
	t.Cleanup(func() { maxStdout, maxStderr = oldOut, oldErr })

	res, err := ExecRunner{}.Run(context.Background(), Cmd{Path: "/usr/bin/head", Args: []string{"-c", "1000", "/dev/zero"}})
	if err != nil || len(res.Stdout) != 1000 {
		t.Fatalf("1000 bytes at a cap of 1000: %d bytes, %v", len(res.Stdout), err)
	}
	res, err = ExecRunner{}.Run(context.Background(), Cmd{Path: "/usr/bin/head", Args: []string{"-c", "5000000", "/dev/zero"}})
	if !errors.Is(err, ErrOutputTooLarge) || !strings.Contains(err.Error(), "/usr/bin/head") {
		t.Errorf("5 MB at a cap of 1000: err = %v, want ErrOutputTooLarge naming the program", err)
	}
	if len(res.Stdout) > 1000 {
		t.Errorf("kept %d bytes of stdout, want at most 1000", len(res.Stdout))
	}

	script := `i=0; while [ $i -lt 200 ]; do echo "line $i" >&2; i=$((i+1)); done; echo "the reason" >&2; exit 3`
	res, err = ExecRunner{}.Run(context.Background(), Cmd{Path: "/bin/sh", Args: []string{"-c", script}})
	var exitErr *ExitError
	if !errors.As(err, &exitErr) || exitErr.Code != 3 {
		t.Fatalf("err = %v, want an ExitError with code 3", err)
	}
	if len(res.Stderr) > 100 || !strings.HasSuffix(string(res.Stderr), "the reason\n") {
		t.Errorf("stderr = %q, want at most 100 bytes ending with the reason", res.Stderr)
	}
}

func TestRunnerContextCancel(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := ExecRunner{}.Run(ctx, Cmd{Path: "/usr/bin/sleep", Args: []string{"10"}})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want context.DeadlineExceeded", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Errorf("Run took %v after the deadline", time.Since(start))
	}
}

func TestFakeRunnerScriptsAndRecords(t *testing.T) {
	f := NewFakeRunner(
		Script{Path: "/usr/sbin/nginx", Args: []string{"-t"}, Result: Result{ExitCode: 1, Stderr: []byte("bad")}},
		Script{Path: "/usr/sbin/nginx", Args: []string{"-t"}, Result: Result{Stderr: []byte("ok")}},
		Script{Path: "/usr/bin/htpasswd", Args: []string{"-i"}, Result: Result{Stdout: []byte("done")}},
	)
	ctx := context.Background()

	_, err := f.Run(ctx, Cmd{Path: "/usr/sbin/nginx", Args: []string{"-t"}})
	var exitErr *ExitError
	if !errors.As(err, &exitErr) || exitErr.Code != 1 {
		t.Errorf("first nginx -t: %v, want ExitError 1", err)
	}
	for range 2 { // the last script for a command repeats
		res, err := f.Run(ctx, Cmd{Path: "/usr/sbin/nginx", Args: []string{"-t"}})
		if err != nil || string(res.Stderr) != "ok" {
			t.Errorf("later nginx -t: %q, %v", res.Stderr, err)
		}
	}
	res, err := f.Run(ctx, Cmd{Path: "/usr/bin/htpasswd", Args: []string{"-i"}, Stdin: strings.NewReader("hash"), Env: []string{"A=1"}})
	if err != nil || string(res.Stdout) != "done" {
		t.Errorf("htpasswd: %q, %v", res.Stdout, err)
	}
	if _, err := f.Run(ctx, Cmd{Path: "/usr/sbin/nginx", Args: []string{"-s", "reload"}}); err == nil {
		t.Error("an unscripted command succeeded")
	}

	calls := f.Calls()
	if len(calls) != 5 {
		t.Fatalf("recorded %d calls, want 5", len(calls))
	}
	if c := calls[3]; c.Path != "/usr/bin/htpasswd" || string(c.Stdin) != "hash" || !slices.Equal(c.Env, []string{"A=1"}) {
		t.Errorf("recorded call = %+v", c)
	}
	if c := calls[4]; c.Path != "/usr/sbin/nginx" || !slices.Equal(c.Args, []string{"-s", "reload"}) {
		t.Errorf("the unscripted call was not recorded: %+v", c)
	}
}

func TestFakeRunnerRequiresAbsolutePath(t *testing.T) {
	f := NewFakeRunner(Script{Path: "nginx", Args: []string{"-t"}})
	if _, err := f.Run(context.Background(), Cmd{Path: "nginx", Args: []string{"-t"}}); err == nil {
		t.Error("FakeRunner accepted a relative path")
	}
}

func TestLockSerialises(t *testing.T) {
	path := filepath.Join(t.TempDir(), "reload.lock")
	unlock, err := Lock(context.Background(), path)
	if err != nil {
		t.Fatalf("first Lock: %v", err)
	}
	fi, err := os.Stat(path)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("lock file: %v, %v; want mode 0600", fi, err)
	}

	acquired := make(chan func(), 1)
	go func() {
		u, err := Lock(context.Background(), path)
		if err != nil {
			t.Errorf("second Lock: %v", err)
			close(acquired)
			return
		}
		acquired <- u
	}()
	select {
	case <-acquired:
		t.Fatal("the second Lock returned while the first was held")
	case <-time.After(300 * time.Millisecond):
	}
	unlock()
	select {
	case u := <-acquired:
		if u != nil {
			u()
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the second Lock did not return after unlock")
	}
}

func TestLockHonoursContext(t *testing.T) {
	path := filepath.Join(t.TempDir(), "site:shop.lock")
	unlock, err := Lock(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if _, err := Lock(ctx, path); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Lock on a held lock = %v, want context.DeadlineExceeded", err)
	}
}

func TestTryLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cancel.lock")
	unlock, ok, err := TryLock(path)
	if err != nil || !ok {
		t.Fatalf("TryLock on a free lock = %v, %v; want it taken", ok, err)
	}
	if _, ok, err := TryLock(path); err != nil || ok {
		t.Errorf("TryLock on a held lock = %v, %v; want false at once", ok, err)
	}
	unlock()
	unlock, ok, err = TryLock(path)
	if err != nil || !ok {
		t.Fatalf("TryLock after unlock = %v, %v; want it taken", ok, err)
	}
	unlock()
}
