package siteprocs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"

	"zimam/agent/contract"
	"zimam/agent/internal/configtx"
	"zimam/agent/internal/helper"
	"zimam/agent/internal/sites"
	"zimam/agent/internal/sysexec"
	"zimam/agent/internal/templates"
)

// cronEnv are the file's environment lines, which cron applies to every job
// in it. The job's own PATH comes from site.env.sh (R04-D15).
var cronEnv = []string{"SHELL=/bin/sh", "PATH=/usr/bin:/bin", `MAILTO=""`}

// renderCron makes a site's cron file from its enabled jobs. A variable so a
// test can make it return a bad file, and see the check refuse it.
var renderCron = func(site contract.SiteName, scheduler bool, jobs []contract.CronJobSpec) ([]byte, error) {
	data := templates.SiteCron{Site: site, Scheduler: scheduler}
	for _, j := range jobs {
		if !j.Enabled {
			continue
		}
		// The parsed schedule, never the stored text: that need not be
		// normal, and the template accepts only the normal form.
		s, err := contract.ParseCronSchedule(string(j.Schedule))
		if err != nil {
			return nil, fmt.Errorf("job %s's schedule %v", j.ID, err)
		}
		data.Jobs = append(data.Jobs, templates.CronLine{ID: j.ID, Schedule: s})
	}
	return templates.Render("cron/site.cron", data)
}

// ReconcileCron is site.cron (R04-D5), under the site's lock. It writes the
// scripts first, then /etc/cron.d/zimam-<site>, so cron never names a script
// that isn't there; the file goes through configtx and an in-agent check
// (cron has no test of its own), and a file that fails it is rolled back.
// With nothing to run, the file is removed first and the scripts after. Cron
// reads a new or changed file within a minute (spike), so nothing is
// reloaded. The jobs, without their commands, are then recorded in the state
// (R04-D16), disabled ones included; `p.Jobs` need not be normalised.
func ReconcileCron(ctx context.Context, d sysexec.Deps, h Helper, a helper.Account, st sites.State, p contract.SiteCronParams) (sites.State, error) {
	site := st.Name
	file := d.Paths.CronDFile(site)
	known := map[contract.WorkerID]bool{}
	for _, j := range p.Jobs {
		if j.Enabled {
			known[j.ID] = true
		}
	}
	if p.Scheduler {
		known[SchedulerID] = true
	}

	if len(known) == 0 {
		if _, err := os.Lstat(file); err == nil {
			d.Logf("Removing the cron file of site %s", site)
			if err := configtx.Apply(ctx, []configtx.Change{{Path: file, Remove: true}}, func(context.Context) error { return nil }); err != nil {
				return st, err
			}
		} else if !errors.Is(err, fs.ErrNotExist) {
			return st, err
		}
		if err := WriteCronScripts(ctx, h, a, st, p); err != nil {
			return st, err
		}
		return recordCron(d, st, p)
	}

	if err := WriteCronScripts(ctx, h, a, st, p); err != nil {
		return st, err
	}
	content, err := renderCron(site, p.Scheduler, p.Jobs)
	if err != nil {
		return st, err
	}
	// An unchanged file is left alone, so a run that failed after it only
	// has the state left to write.
	old, err := os.ReadFile(file)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return st, err
	}
	if err != nil || !bytes.Equal(old, content) {
		d.Logf("Writing the cron file of site %s", site)
		err := configtx.Apply(ctx, []configtx.Change{configtx.RootFile(file, content, 0o644)}, func(context.Context) error {
			// What is on disk is what cron would read. After a rollback it
			// is the previous file, which passed this check when it was
			// written, so only the new one is checked.
			b, err := os.ReadFile(file)
			if err != nil || !bytes.Equal(b, content) {
				return nil
			}
			return checkCronFile(site, b, known)
		})
		if err != nil {
			return st, err
		}
	}
	return recordCron(d, st, p)
}

// checkCronFile is the test of a cron file: the file's lines are read back
// and nothing but what the template makes is accepted. It ends with a
// newline (cron ignores a file without one, spike); the name has no dot; the
// first line is the template's comment, then exactly the three environment
// lines; every other line is a schedule in normal form, the site's own user
// and /bin/bash on the script of a job in ids, with nothing after it.
func checkCronFile(site contract.SiteName, content []byte, ids map[contract.WorkerID]bool) error {
	if strings.Contains(string(site), ".") {
		return fmt.Errorf("cron ignores the file of a site named %q: a name with a dot", site)
	}
	text := string(content)
	if !strings.HasSuffix(text, "\n") {
		return errors.New("the cron file doesn't end with a newline, so cron would ignore it")
	}
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	if len(lines) < 1+len(cronEnv) || !strings.HasPrefix(lines[0], "# ") {
		return errors.New("the cron file doesn't start with its comment and environment lines")
	}
	for i, want := range cronEnv {
		if lines[1+i] != want {
			return fmt.Errorf("environment line %q, want %q", lines[1+i], want)
		}
	}
	prefix := "s_" + string(site) + " /bin/bash /home/s_" + string(site) + "/.zimam/cron/"
	seen := map[contract.WorkerID]bool{}
	for _, line := range lines[1+len(cronEnv):] {
		f := strings.SplitN(line, " ", 6)
		if len(f) != 6 {
			return fmt.Errorf("line %q isn't a schedule and a command", line)
		}
		schedule := strings.Join(f[:5], " ")
		if n, err := contract.ParseCronSchedule(schedule); err != nil || string(n) != schedule {
			return fmt.Errorf("line %q: %q isn't a schedule in normal form", line, schedule)
		}
		name, ok := strings.CutPrefix(f[5], prefix)
		name, ok2 := strings.CutSuffix(name, ".sh")
		id := contract.WorkerID(name)
		if !ok || !ok2 || !ids[id] {
			return fmt.Errorf("line %q doesn't run exactly the script of a job of the site as the site's user", line)
		}
		if seen[id] {
			return fmt.Errorf("job %s has two lines", id)
		}
		seen[id] = true
	}
	if len(seen) != len(ids) {
		return fmt.Errorf("the file has %d lines for %d jobs", len(seen), len(ids))
	}
	return nil
}

// recordCron keeps the jobs in the state (R04-D16), as the panel gave them
// but without their commands; a site with no job and no scheduler has none.
func recordCron(d sysexec.Deps, st sites.State, p contract.SiteCronParams) (sites.State, error) {
	st.Cron = nil
	if p.Scheduler || len(p.Jobs) > 0 {
		c := &sites.CronState{Scheduler: p.Scheduler, Jobs: []sites.CronJobState{}}
		for _, j := range p.Jobs {
			c.Jobs = append(c.Jobs, sites.CronJobState{ID: j.ID, Schedule: j.Schedule, Enabled: j.Enabled})
		}
		st.Cron = c
	}
	if err := sites.Save(d.Paths, st); err != nil {
		return st, fmt.Errorf("the cron jobs run, but recording them in the state failed; run the command again: %w", err)
	}
	d.Logf("Site %s has %d cron jobs", st.Name, len(p.Jobs))
	return st, nil
}
