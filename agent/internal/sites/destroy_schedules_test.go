package sites

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"zimam/agent/internal/sysexec"
)

const atqCmd = "/usr/bin/atq"

func scriptOut(cmd, stdout string) sysexec.Script {
	f := strings.Fields(cmd)
	return sysexec.Script{Path: f[0], Args: f[1:], Result: sysexec.Result{Stdout: []byte(stdout)}}
}

// install makes the program at each path exist, as its package does.
func install(t *testing.T, paths ...string) {
	t.Helper()
	for _, path := range paths {
		mkdir(t, filepath.Dir(path))
		writeFile(t, path, "")
	}
}

const atJobs = "3\tFri Nov 14 10:00:00 2026 a s_shop\n" +
	"4\tFri Nov 14 11:00:00 2026 a root\n" +
	"5\tFri Nov 14 12:00:00 2026 a s_shop2\n" +
	"6\tFri Nov 14 13:00:00 2026 b s_shop\n" +
	"7\tFri Nov 14 14:00:00 2026 a s_sho\n"

// The crontab is deleted from cron's spool, not with crontab -r -u, which
// refuses every user /etc/cron.allow doesn't list: since v0.4.0 that is
// every site user, so site.destroy failed on every site (found by the e2e
// run). A crontab left from before v0.4.0 still goes, another user's stays.
func TestDestroyRemovesCrontabFromTheSpool(t *testing.T) {
	e := provisioned(t)
	install(t, e.p.Crontab())
	spool, other := e.p.CrontabSpool("s_shop"), e.p.CrontabSpool("s_shop2")
	mkdir(t, filepath.Dir(spool))
	writeFile(t, spool, "* * * * * true\n")
	writeFile(t, other, "* * * * * true\n")
	n := len(e.run.Calls())
	if _, err := e.destroy(); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	if _, err := os.Lstat(spool); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("s_shop's crontab is still in the spool: %v", err)
	}
	if _, err := os.Lstat(other); err != nil {
		t.Errorf("s_shop2's crontab went too: %v", err)
	}
	for _, c := range e.callsSince(n) {
		if strings.Contains(c, "crontab") {
			t.Errorf("ran %q; crontab refuses a user cron.allow doesn't list", c)
		}
	}
}

// spyRunner calls before ahead of every command it runs.
type spyRunner struct {
	sysexec.Runner
	before func(sysexec.Cmd)
}

func (s spyRunner) Run(ctx context.Context, c sysexec.Cmd) (sysexec.Result, error) {
	s.before(c)
	return s.Runner.Run(ctx, c)
}

// The crontab goes before the processes are killed and before userdel, so
// cron can't start a job in between that makes userdel fail with "currently
// used by process". A removal that fails for another reason than a missing
// file stops site.destroy there, before anything is killed.
func TestDestroyRemovesCrontabBeforeKillAndUserdel(t *testing.T) {
	e := provisioned(t)
	install(t, e.p.Crontab())
	spool := e.p.CrontabSpool("s_shop")
	mkdir(t, filepath.Dir(spool))
	writeFile(t, spool, "* * * * * true\n")
	e.d.Run = spyRunner{e.d.Run, func(c sysexec.Cmd) {
		if c.Path != "/usr/bin/pkill" && c.Path != "/usr/sbin/userdel" {
			return
		}
		if _, err := os.Lstat(spool); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("%s ran with the crontab still in the spool: %v", c.Path, err)
		}
	}}
	if _, err := e.destroy(); err != nil {
		t.Fatalf("Destroy: %v", err)
	}

	// A spool entry that can't be removed: a directory with a file in it.
	e = provisioned(t)
	install(t, e.p.Crontab())
	spool = e.p.CrontabSpool("s_shop")
	mkdir(t, spool)
	writeFile(t, filepath.Join(spool, "x"), "")
	n := len(e.run.Calls())
	if _, err := e.destroy(); err == nil || !strings.Contains(err.Error(), "removing the crontab of s_shop") {
		t.Fatalf("Destroy = %v, want the crontab removal's error", err)
	}
	for _, c := range e.callsSince(n) {
		if c == pkillCmd || c == userdelCmd {
			t.Errorf("ran %q after the crontab removal failed", c)
		}
	}
}

func TestDestroyRemovesAtJobsOfThatUserOnly(t *testing.T) {
	e := provisioned(t)
	install(t, e.p.Atq())
	e.run.Add(scriptOut(atqCmd, atJobs))
	e.run.Add(scriptOut("/usr/bin/atrm 3 6", ""))
	n := len(e.run.Calls())
	if _, err := e.destroy(); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	got := e.callsSince(n)
	i := slices.Index(got, "/usr/bin/atrm 3 6")
	if i < 0 {
		t.Fatalf("atrm 3 6 didn't run:\n%s", strings.Join(got, "\n"))
	}
	if i > slices.Index(got, userdelCmd) {
		t.Error("the at jobs went after userdel, when atq no longer names the user")
	}
}

// s_shop's jobs and s_shop2's are told apart by the whole name.
func TestAtFilterMatchesWholeUserName(t *testing.T) {
	e := provisioned(t)
	install(t, e.p.Atq())
	e.run.Add(scriptOut(atqCmd, "9\tFri Nov 14 10:00:00 2026 a s_shop2\n10\tFri Nov 14 10:00:00 2026 a s_shop\n11\tFri Nov 14 10:00:00 2026 a s_shop22\n"))
	e.run.Add(scriptOut("/usr/bin/atrm 10", ""))
	n := len(e.run.Calls())
	if _, err := e.destroy(); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	for _, c := range e.callsSince(n) {
		if strings.HasPrefix(c, "/usr/bin/atrm") && c != "/usr/bin/atrm 10" {
			t.Errorf("ran %q; only s_shop's job 10 may go", c)
		}
	}
	if !slices.Contains(e.callsSince(n), "/usr/bin/atrm 10") {
		t.Error("s_shop's job wasn't removed")
	}

	// No job of the user: no atrm at all.
	e = provisioned(t)
	install(t, e.p.Atq())
	e.run.Add(scriptOut(atqCmd, "9\tFri Nov 14 10:00:00 2026 a s_shop2\n"))
	n = len(e.run.Calls())
	if _, err := e.destroy(); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	for _, c := range e.callsSince(n) {
		if strings.HasPrefix(c, "/usr/bin/atrm") {
			t.Errorf("ran %q with no job of s_shop", c)
		}
	}
}

func TestDestroyWithoutCronOrAtSkipsThem(t *testing.T) {
	e := provisioned(t)
	n := len(e.run.Calls())
	if _, err := e.destroy(); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	for _, c := range e.callsSince(n) {
		if strings.Contains(c, "crontab") || strings.Contains(c, "atq") || strings.Contains(c, "atrm") {
			t.Errorf("ran %q with cron and at absent", c)
		}
	}
}

func TestDestroyNoCrontabIsNotAnError(t *testing.T) {
	e := provisioned(t)
	install(t, e.p.Crontab())
	if _, err := e.destroy(); err != nil {
		t.Fatalf("Destroy with no crontab: %v", err)
	}
}
