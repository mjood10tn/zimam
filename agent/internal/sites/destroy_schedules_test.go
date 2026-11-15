package sites

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"zimam/agent/internal/sysexec"
)

const (
	crontabCmd = "/usr/bin/crontab -r -u s_shop"
	atqCmd     = "/usr/bin/atq"
)

func scriptOut(cmd, stdout string) sysexec.Script {
	f := strings.Fields(cmd)
	return sysexec.Script{Path: f[0], Args: f[1:], Result: sysexec.Result{Stdout: []byte(stdout)}}
}

func scriptFail(cmd string, code int, stderr string) sysexec.Script {
	f := strings.Fields(cmd)
	return sysexec.Script{Path: f[0], Args: f[1:], Result: sysexec.Result{ExitCode: code, Stderr: []byte(stderr)}}
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

func TestDestroyRemovesCrontabBeforeUserdel(t *testing.T) {
	e := provisioned(t)
	install(t, e.p.Crontab())
	e.run.Add(scriptOut(crontabCmd, ""))
	n := len(e.run.Calls())
	if _, err := e.destroy(); err != nil {
		t.Fatalf("Destroy: %v", err)
	}
	got := e.callsSince(n)
	ci, ki, ui := slices.Index(got, crontabCmd), slices.Index(got, pgrepCmd), slices.Index(got, userdelCmd)
	if ci < 0 || ci < ki || ci > ui {
		t.Errorf("crontab -r must run after the processes are killed and before userdel:\n%s", strings.Join(got, "\n"))
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
	e.run.Add(scriptFail(crontabCmd, 1, "no crontab for s_shop"))
	if _, err := e.destroy(); err != nil {
		t.Fatalf("Destroy with no crontab: %v", err)
	}

	e = provisioned(t)
	install(t, e.p.Crontab())
	e.run.Add(scriptFail(crontabCmd, 1, "crontab: cannot open spool"))
	if _, err := e.destroy(); err == nil {
		t.Fatal("a crontab failure other than no crontab was ignored")
	}
}
