package siteprocs

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"zimam/agent/contract"
)

func (f *fixture) cronFile() string { return f.d.Paths.CronDFile("shop") }

func (f *fixture) cron(t *testing.T, p contract.SiteCronParams) error {
	t.Helper()
	p.Name = "shop"
	if err := os.MkdirAll(f.d.Paths.CronDDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	st, err := ReconcileCron(context.Background(), f.d, f.h, acct, f.st, p)
	if err == nil {
		f.st = st
	}
	return err
}

func (f *fixture) cronContent(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(f.cronFile())
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func job(id, schedule string, enabled bool) contract.CronJobSpec {
	return contract.CronJobSpec{ID: contract.WorkerID(id), Schedule: contract.CronSchedule(schedule), Command: "php artisan " + contract.OwnerCommand(id), Enabled: enabled}
}

const cronHead = "# Managed by Zimam for site shop. Changes here are overwritten.\nSHELL=/bin/sh\nPATH=/usr/bin:/bin\nMAILTO=\"\"\n"

// Every line is the one the template makes: the schedule normalised from the
// parsed value, the site's own user and a script in its home. A test parser
// reads each back.
func TestCronFileHasOnlyRenderedFields(t *testing.T) {
	f := newFixture(t)
	// A stored schedule need not be normal: a macro and a named weekday.
	if err := f.cron(t, contract.SiteCronParams{Scheduler: true, Jobs: contract.CronJobList{
		job("backup", "0 3 * * MON", true), job("tidy", "@daily", true),
	}}); err != nil {
		t.Fatal(err)
	}
	want := cronHead +
		"* * * * * s_shop /bin/bash /home/s_shop/.zimam/cron/scheduler.sh\n" +
		"0 3 * * 1 s_shop /bin/bash /home/s_shop/.zimam/cron/backup.sh\n" +
		"0 0 * * * s_shop /bin/bash /home/s_shop/.zimam/cron/tidy.sh\n"
	got := f.cronContent(t)
	if got != want {
		t.Errorf("cron file:\n%s\nwant:\n%s", got, want)
	}
	if err := checkCronFile("shop", []byte(got), map[contract.WorkerID]bool{"backup": true, "tidy": true, SchedulerID: true}); err != nil {
		t.Errorf("the checker refuses the file it should accept: %v", err)
	}
	fi, err := os.Stat(f.cronFile())
	if err != nil || fi.Mode().Perm() != 0o644 {
		t.Errorf("mode = %v, %v; want 0644", fi.Mode().Perm(), err)
	}
	if f.st.Cron == nil || !f.st.Cron.Scheduler || len(f.st.Cron.Jobs) != 2 || f.st.Cron.Jobs[0].Schedule != "0 3 * * MON" {
		t.Errorf("state cron = %+v", f.st.Cron)
	}
}

func TestDisabledJobOmitted(t *testing.T) {
	f := newFixture(t)
	if err := f.cron(t, contract.SiteCronParams{Jobs: contract.CronJobList{job("a", "* * * * *", true), job("b", "0 * * * *", false)}}); err != nil {
		t.Fatal(err)
	}
	got := f.cronContent(t)
	if !strings.Contains(got, "/cron/a.sh\n") || strings.Contains(got, "b.sh") || strings.Contains(got, "scheduler") {
		t.Errorf("cron file:\n%s", got)
	}
	if _, err := os.Stat(filepath.Join(f.h.home, ".zimam/cron/b.sh")); err == nil {
		t.Error("the disabled job has a script")
	}
	// A disabled job is still in the state, for the panel to list.
	if len(f.st.Cron.Jobs) != 2 || f.st.Cron.Jobs[1].Enabled {
		t.Errorf("state cron = %+v", f.st.Cron)
	}
}

// An empty list with the scheduler off removes the file, and the scripts.
func TestEmptyCronRemovesFile(t *testing.T) {
	f := newFixture(t)
	if err := f.cron(t, contract.SiteCronParams{Scheduler: true, Jobs: contract.CronJobList{job("a", "* * * * *", true)}}); err != nil {
		t.Fatal(err)
	}
	if err := f.cron(t, contract.SiteCronParams{Jobs: contract.CronJobList{}}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(f.cronFile()); !os.IsNotExist(err) {
		t.Errorf("the cron file is left: %v", err)
	}
	for _, s := range []string{"a.sh", "scheduler.sh"} {
		if _, err := os.Stat(filepath.Join(f.h.home, ".zimam/cron", s)); !os.IsNotExist(err) {
			t.Errorf("%s is left: %v", s, err)
		}
	}
	if f.st.Cron != nil {
		t.Errorf("state cron = %+v, want none", f.st.Cron)
	}
	// Removing again is no error.
	if err := f.cron(t, contract.SiteCronParams{Jobs: contract.CronJobList{}}); err != nil {
		t.Fatal(err)
	}
	// Only disabled jobs leave no line either: the file goes, the state keeps them.
	if err := f.cron(t, contract.SiteCronParams{Jobs: contract.CronJobList{job("a", "* * * * *", false)}}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(f.cronFile()); !os.IsNotExist(err) {
		t.Errorf("the cron file exists with no line to run: %v", err)
	}
	if f.st.Cron == nil || len(f.st.Cron.Jobs) != 1 {
		t.Errorf("state cron = %+v", f.st.Cron)
	}
}

func TestCronFileNameHasNoDot(t *testing.T) {
	f := newFixture(t)
	if base := filepath.Base(f.cronFile()); base != "zimam-shop" {
		t.Errorf("file name %q", base)
	}
	if err := checkCronFile("sh.op", []byte(cronHead), nil); err == nil {
		t.Error("a site name with a dot was accepted: cron would ignore the file")
	}
}

// A file that fails the check is never left in place: the previous one is
// restored, and the state is not changed. The scripts are written first, so
// cron never names a missing one.
func TestBadRenderRestoresPrevious(t *testing.T) {
	f := newFixture(t)
	if err := f.cron(t, contract.SiteCronParams{Jobs: contract.CronJobList{job("a", "* * * * *", true)}}); err != nil {
		t.Fatal(err)
	}
	before := f.cronContent(t)
	stBefore := *f.st.Cron
	orig := renderCron
	t.Cleanup(func() { renderCron = orig })
	renderCron = func(site contract.SiteName, scheduler bool, jobs []contract.CronJobSpec) ([]byte, error) {
		return []byte(cronHead + "* * * * * root /bin/bash /home/s_shop/.zimam/cron/b.sh\n"), nil
	}
	err := f.cron(t, contract.SiteCronParams{Jobs: contract.CronJobList{job("a", "* * * * *", true), job("b", "0 * * * *", true)}})
	if err == nil {
		t.Fatal("a file naming root was accepted")
	}
	if got := f.cronContent(t); got != before {
		t.Errorf("the previous file wasn't restored:\n%s", got)
	}
	if f.st.Cron == nil || len(f.st.Cron.Jobs) != len(stBefore.Jobs) {
		t.Errorf("the state changed: %+v", f.st.Cron)
	}
	if _, err := os.Stat(filepath.Join(f.h.home, ".zimam/cron/b.sh")); err != nil {
		t.Errorf("scripts were not written before the file: %v", err)
	}
}

func TestCheckCronFileRefuses(t *testing.T) {
	ids := map[contract.WorkerID]bool{"a": true}
	line := "* * * * * s_shop /bin/bash /home/s_shop/.zimam/cron/a.sh\n"
	if err := checkCronFile("shop", []byte(cronHead+line), ids); err != nil {
		t.Fatalf("the good file: %v", err)
	}
	for name, content := range map[string]string{
		"no final newline":    strings.TrimSuffix(cronHead+line, "\n"),
		"other user":          cronHead + "* * * * * root /bin/bash /home/s_shop/.zimam/cron/a.sh\n",
		"other site's user":   cronHead + "* * * * * s_shop2 /bin/bash /home/s_shop/.zimam/cron/a.sh\n",
		"unknown id":          cronHead + "* * * * * s_shop /bin/bash /home/s_shop/.zimam/cron/z.sh\n",
		"other command":       cronHead + "* * * * * s_shop /bin/bash /home/s_shop/.zimam/cron/a.sh; id\n",
		"path outside":        cronHead + "* * * * * s_shop /bin/bash /home/s_shop/.zimam/cron/../a.sh\n",
		"macro":               cronHead + "@daily s_shop /bin/bash /home/s_shop/.zimam/cron/a.sh\n",
		"unnormal schedule":   cronHead + "0 3 * * MON s_shop /bin/bash /home/s_shop/.zimam/cron/a.sh\n",
		"bad schedule":        cronHead + "61 * * * * s_shop /bin/bash /home/s_shop/.zimam/cron/a.sh\n",
		"extra env":           cronHead + "HOME=/root\n" + line,
		"changed env":         strings.Replace(cronHead, "SHELL=/bin/sh", "SHELL=/bin/bash", 1) + line,
		"a missing env":       strings.Replace(cronHead, "MAILTO=\"\"\n", "", 1) + line,
		"a job not in file":   cronHead,
		"carriage return":     cronHead + "* * * * * s_shop /bin/bash /home/s_shop/.zimam/cron/a.sh\r\n",
		"a job listed twice":  cronHead + line + line,
		"a blank line inside": cronHead + "\n" + line,
	} {
		if err := checkCronFile("shop", []byte(content), ids); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
