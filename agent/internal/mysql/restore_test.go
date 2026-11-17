package mysql

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"zimam/agent/contract"
	"zimam/agent/internal/sysexec"
)

// A converged server keeps running: the file is already in place and the
// live values already match, so nothing restarts.
func TestInstallConvergedServerIsNotRestarted(t *testing.T) {
	e := newEnv(t, gib/1024)
	if err := os.WriteFile(e.p.MySQLConf(), []byte(wantConf), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, s := range []sysexec.Script{aptUpdate, aptInstall, client("auth_socket\n"), validate, client(liveGood)} {
		e.run.Add(s)
	}
	if _, err := Install(context.Background(), e.d); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if slices.Contains(lines(e.run.Calls()), line(restart)) {
		t.Error("MySQL was restarted although nothing changed")
	}
}

// With the file in place but the server not answering, MySQL restarts.
func TestInstallRestartsWhenAnUnchangedFileIsNotLive(t *testing.T) {
	e := newEnv(t, gib/1024)
	if err := os.WriteFile(e.p.MySQLConf(), []byte(wantConf), 0o644); err != nil {
		t.Fatal(err)
	}
	down := failing(client(""), 1, "ERROR 2002 (HY000): Can't connect to local MySQL server through socket\n")
	for _, s := range []sysexec.Script{aptUpdate, aptInstall, client("auth_socket\n"), validate, down, restart, client(liveGood)} {
		e.run.Add(s)
	}
	if _, err := Install(context.Background(), e.d); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if !slices.Contains(lines(e.run.Calls()), line(restart)) {
		t.Error("MySQL was not restarted")
	}
}

const badBind = "0.0.0.0\t0\t1\t/var/lib/mysql-files/\t268435456\t0\tDISABLED\t8.4.13-0ubuntu0.26.04.2\n"

// A previous file equal to the new one that still fails the live check:
// the restore pass only puts things back, and the error is the first one.
func TestEqualPreviousFileFailingLiveCheck(t *testing.T) {
	e := newEnv(t, gib/1024)
	if err := os.WriteFile(e.p.MySQLConf(), []byte(wantConf), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, s := range []sysexec.Script{aptUpdate, aptInstall, client("auth_socket\n"), validate, restart, client(badBind)} {
		e.run.Add(s)
	}
	_, err := Install(context.Background(), e.d)
	if err == nil || !strings.Contains(err.Error(), "bind_address") || strings.Contains(err.Error(), "also fails") {
		t.Fatalf("Install = %v, want only the first failure", err)
	}
	c := line(client(""))
	want := []string{line(aptUpdate), line(aptInstall), c, line(validate), c, line(restart), c, line(validate), line(restart)}
	if got := lines(e.run.Calls()); !slices.Equal(got, want) {
		t.Errorf("ran\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// root@localhost with a password refuses the socket login. That is found
// before any file is written or MySQL restarts, and says what to do.
func TestRootPasswordIsRefusedBeforeAnyChange(t *testing.T) {
	e := newEnv(t, gib/1024)
	withPrevious(t, e)
	e.run.Add(aptUpdate)
	e.run.Add(aptInstall)
	e.run.Add(failing(client(""), 1, "ERROR 1045 (28000): Access denied for user 'root'@'localhost' (using password: NO)\n"))
	_, err := Install(context.Background(), e.d)
	var ae *contract.AgentError
	if !errors.As(err, &ae) || ae.Code != contract.ErrConflict || !strings.Contains(ae.Message, "auth_socket") || !strings.Contains(ae.Message, "password") {
		t.Fatalf("Install = %v, want a conflict about the root password and auth_socket", err)
	}
	want := []string{line(aptUpdate), line(aptInstall), line(client(""))}
	if got := lines(e.run.Calls()); !slices.Equal(got, want) {
		t.Errorf("ran\n%s\nwant only the packages and the root check", strings.Join(got, "\n"))
	}
	if got := readFile(t, e.p.MySQLConf()); got != previous {
		t.Errorf("the config file changed:\n%s", got)
	}
	if _, err := os.Lstat(e.p.ServicesState()); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("services.json was written (Lstat: %v)", err)
	}
}

func TestRootCheckFailingForAnotherReasonChangesNothing(t *testing.T) {
	e := newEnv(t, gib/1024)
	e.run.Add(aptUpdate)
	e.run.Add(aptInstall)
	e.run.Add(failing(client(""), 1, "ERROR 2002 (HY000): Can't connect to local MySQL server through socket\n"))
	if _, err := Install(context.Background(), e.d); err == nil || !strings.Contains(err.Error(), "Can't connect") {
		t.Fatalf("Install = %v", err)
	}
	if _, err := os.Lstat(e.p.MySQLConf()); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the config file was written (Lstat: %v)", err)
	}
	if len(e.run.Calls()) != 3 {
		t.Errorf("ran %d commands, want 3", len(e.run.Calls()))
	}
}

// ctxRecorder cancels the job's context during the candidate's restart, as
// stopping the job's unit does, and records which later validate and restart
// ran with a live context.
type ctxRecorder struct {
	sysexec.Runner
	cancel    context.CancelFunc
	cancelled bool
	later     []string
}

func (r *ctxRecorder) Run(ctx context.Context, c sysexec.Cmd) (sysexec.Result, error) {
	isRestart := c.Path == systemctl && slices.Equal(c.Args, restart.Args)
	if isRestart && !r.cancelled {
		r.cancelled = true
		r.cancel()
		return sysexec.Result{}, context.Canceled
	}
	if r.cancelled && (isRestart || c.Path == mysqld) && ctx.Err() == nil {
		r.later = append(r.later, filepath.Base(c.Path))
	}
	return r.Runner.Run(ctx, c)
}

func TestCancelledJobStillRestoresTheServer(t *testing.T) {
	e := newEnv(t, gib/1024)
	withPrevious(t, e)
	for _, s := range []sysexec.Script{aptUpdate, aptInstall, client("auth_socket\n"), validate, restart} {
		e.run.Add(s)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := &ctxRecorder{Runner: e.run, cancel: cancel}
	e.d.Run = r
	if _, err := Install(ctx, e.d); err == nil {
		t.Fatal("Install succeeded after its context was cancelled")
	}
	if got := readFile(t, e.p.MySQLConf()); got != previous {
		t.Errorf("zz-zimam.cnf = %q, want the previous file back", got)
	}
	if want := []string{"mysqld", "systemctl"}; !slices.Equal(r.later, want) {
		t.Errorf("after the cancel, validate and restart ran with a live context as %v, want %v", r.later, want)
	}
}

func TestFailedRestoreSaysTheServerMayRunOtherSettings(t *testing.T) {
	for name, tc := range map[string]struct {
		scripts []sysexec.Script
		calls   []string // after the packages and the root check
	}{
		"the restart fails": {
			scripts: []sysexec.Script{validate, restart, client(badBind), validate, failing(restart, 1, "Job for mysql.service failed.\n")},
			calls:   []string{line(validate), line(restart), line(client("")), line(validate), line(restart)},
		},
		"the restored file fails its test": {
			scripts: []sysexec.Script{validate, restart, client(badBind), failing(validate, 1, "[ERROR] bad previous file\n")},
			calls:   []string{line(validate), line(restart), line(client("")), line(validate)},
		},
	} {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t, gib/1024)
			withPrevious(t, e)
			for _, s := range append([]sysexec.Script{aptUpdate, aptInstall, client("auth_socket\n")}, tc.scripts...) {
				e.run.Add(s)
			}
			_, err := Install(context.Background(), e.d)
			if err == nil || !strings.Contains(err.Error(), "may be running settings that are not in") || !strings.Contains(err.Error(), "bind_address") {
				t.Fatalf("Install = %v, want the first failure and a warning about the server's settings", err)
			}
			if got := readFile(t, e.p.MySQLConf()); got != previous {
				t.Errorf("the previous file is not back:\n%s", got)
			}
			if got := lines(e.run.Calls())[3:]; !slices.Equal(got, tc.calls) {
				t.Errorf("ran\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(tc.calls, "\n"))
			}
		})
	}
}
