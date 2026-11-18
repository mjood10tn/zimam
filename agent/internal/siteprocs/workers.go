package siteprocs

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"slices"

	"zimam/agent/contract"
	"zimam/agent/internal/configtx"
	"zimam/agent/internal/helper"
	"zimam/agent/internal/sites"
	"zimam/agent/internal/sysexec"
	"zimam/agent/internal/systemd"
	"zimam/agent/internal/templates"
)

const systemdAnalyze = "/usr/bin/systemd-analyze"

// laravelTimeout is queue:work's own --timeout when a worker sets none.
const laravelTimeout = 60

// stopGrace is what a worker gets beyond its timeout to finish its job after
// SIGTERM (R04-D2): TimeoutStopSec is timeout + 30, so 90 without a timeout.
func stopGrace(w contract.WorkerSpec) int {
	t := laravelTimeout
	if w.Timeout != nil {
		t = *w.Timeout
	}
	return t + 30
}

func renderUnit(site contract.SiteName, w contract.WorkerSpec) ([]byte, error) {
	return templates.Render("systemd/zimam-worker.service", templates.WorkerUnit{Site: site, ID: w.ID, MemoryMB: w.Memory(), TimeoutStopSec: stopGrace(w)})
}

// Reconcile is site.workers (R04-D3), under the site's lock (the job's
// subject). It makes the site's units and scripts match workers:
//
//  1. render every wanted unit and the target, and find on disk the units
//     no longer wanted;
//  2. stop and disable the unwanted ones, and every instance above each
//     worker's count (@n+1..@10);
//  3. write the changed unit files and delete the unwanted, tested with
//     systemd-analyze verify --recursive-errors=no (plain verify passes a
//     bad directive, spike); a failed test restores the previous files;
//  4. write the scripts, deleting the unwanted ones;
//  5. one daemon-reload, always;
//  6. restart, gracefully, each running process whose unit file or script
//     is newer than its start, and enable and start every wanted instance
//     by name, which leaves a running unchanged one alone;
//  7. record the workers, without their commands, in the state (R04-D16).
//
// Nothing here trusts the state or this run's own diff alone: removals and
// surplus come from disk and the  bound, the reload always runs, and a
// restart is decided by file times against each process's start. So a run
// that failed half way is finished by running it again.
func Reconcile(ctx context.Context, d sysexec.Deps, h Helper, a helper.Account, st sites.State, workers []contract.WorkerSpec) (sites.State, error) {
	site := st.Name
	if len(workers) == 0 {
		if err := systemd.RemoveSiteUnits(ctx, d, site); err != nil {
			return st, err
		}
		if err := WriteWorkerScripts(ctx, h, a, st, nil); err != nil {
			return st, err
		}
		return record(d, st, nil)
	}

	// Step 1.
	onDisk, err := systemd.WorkerIDs(d, site)
	if err != nil {
		return st, err
	}
	var changes []configtx.Change
	verify := []string{"verify", "--recursive-errors=no"}
	// want queues a unit file that differs from what is on disk; one that is
	// the same is left alone, so its modification time stays (step 6).
	want := func(path string, content []byte) error {
		verify = append(verify, path)
		old, err := os.ReadFile(path)
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		if err != nil || !bytes.Equal(old, content) {
			changes = append(changes, configtx.RootFile(path, content, 0o644))
		}
		return nil
	}
	target, err := templates.Render("systemd/zimam-site.target", templates.SiteTarget{Site: site})
	if err != nil {
		return st, err
	}
	if err := want(d.Paths.SystemdUnit(systemd.SiteTarget(site)), target); err != nil {
		return st, err
	}
	wanted := map[contract.WorkerID]bool{}
	for _, w := range workers {
		wanted[w.ID] = true
		unit, err := renderUnit(site, w)
		if err != nil {
			return st, fmt.Errorf("worker %s's unit: %w", w.ID, err)
		}
		if err := want(d.Paths.SystemdUnit(systemd.WorkerUnit(site, w.ID)), unit); err != nil {
			return st, err
		}
	}
	var removed []contract.WorkerID
	for _, id := range onDisk {
		if !wanted[id] {
			removed = append(removed, id)
			changes = append(changes, configtx.Change{Path: d.Paths.SystemdUnit(systemd.WorkerUnit(site, id)), Remove: true})
		}
	}

	// Step 2.
	if err := systemd.DisableWorkers(ctx, d, site, removed); err != nil {
		return st, err
	}
	// Every instance above a worker's count is stopped, whatever the state
	// says: a run that started more and failed before recording them leaves
	// no process behind. systemctl answers 0 for one that never ran.
	var surplus []string
	for _, w := range workers {
		if slices.Contains(onDisk, w.ID) && w.Processes < contract.MaxWorkerProcesses {
			surplus = append(surplus, systemd.WorkerInstances(site, w.ID, w.Processes+1, contract.MaxWorkerProcesses)...)
		}
	}
	if len(surplus) > 0 {
		d.Logf("Making sure no process runs beyond each worker's count")
		if err := systemd.Systemctl(ctx, d, append([]string{"disable", "--now", "--no-block"}, surplus...)...); err != nil {
			return st, err
		}
	}

	// Step 3.
	if len(changes) > 0 {
		d.Logf("Writing %d unit changes for site %s, then running systemd-analyze verify", len(changes), site)
		err := configtx.Apply(ctx, changes, func(ctx context.Context) error {
			return configtx.RunTest(ctx, d.Run, sysexec.Cmd{Path: systemdAnalyze, Args: verify})
		})
		if err != nil {
			return st, err
		}
	}

	// Step 4.
	if err := WriteWorkerScripts(ctx, h, a, st, workers); err != nil {
		return st, err
	}

	// Step 5, always: a run that wrote a unit and failed before reloading
	// leaves nothing on disk to tell the next one.
	if err := systemd.Systemctl(ctx, d, "daemon-reload"); err != nil {
		return st, err
	}

	// Step 6. A running process restarts when its unit file or script is
	// newer than its start, which also catches a change an earlier run wrote
	// and failed to restart. Unchanged files are never rewritten (want,
	// writeScripts), so their times stay put.
	changedAt, err := changeTimes(ctx, d, h, a, site, workers)
	if err != nil {
		return st, err
	}
	var states []sites.WorkerState
	var all []string
	for _, w := range workers {
		states = append(states, sites.NewWorkerState(w))
		all = append(all, systemd.WorkerInstances(site, w.ID, 1, w.Processes)...)
	}
	blocks, err := show(ctx, d, "Id,ActiveState,ExecMainStartTimestamp", all)
	if err != nil {
		return st, err
	}
	if err := resetFailed(ctx, d, blocks); err != nil {
		return st, err
	}
	var restart, start []string
	i := 0
	for _, w := range workers {
		for range w.Processes {
			b := blocks[i]
			i++
			started := unixSeconds(b["ExecMainStartTimestamp"])
			if running(b["ActiveState"]) && started > 0 && changedAt[w.ID] > started {
				restart = append(restart, b["Id"])
			} else {
				start = append(start, b["Id"])
			}
		}
	}
	if err := systemd.Systemctl(ctx, d, append([]string{"enable", systemd.SiteTarget(site)}, all...)...); err != nil {
		return st, err
	}
	if len(restart) > 0 {
		d.Logf("Restarting %d changed processes gracefully", len(restart))
		if err := systemd.Systemctl(ctx, d, append([]string{"restart", "--no-block"}, restart...)...); err != nil {
			return st, err
		}
	}
	// Queued like the rest: a start waiting behind a graceful stop could
	// outlast the job.
	if err := systemd.Systemctl(ctx, d, append([]string{"start", "--no-block", systemd.SiteTarget(site)}, start...)...); err != nil {
		return st, err
	}

	// Step 7.
	return record(d, st, states)
}

func running(active string) bool {
	return active == "active" || active == "activating" || active == "reloading"
}

// changeTimes is, per worker, the later of its unit file's and its script's
// modification time, in Unix seconds. The script's comes from the helper's
// listing, as the site user.
func changeTimes(ctx context.Context, d sysexec.Deps, h Helper, a helper.Account, site contract.SiteName, workers []contract.WorkerSpec) (map[contract.WorkerID]int64, error) {
	l, err := h.List(ctx, a, workersDir)
	if err != nil {
		return nil, err
	}
	scripts := map[string]int64{}
	for _, e := range l.Entries {
		scripts[e.Name] = e.ModTime.Unix()
	}
	at := map[contract.WorkerID]int64{}
	for _, w := range workers {
		fi, err := os.Stat(d.Paths.SystemdUnit(systemd.WorkerUnit(site, w.ID)))
		if err != nil {
			return nil, err
		}
		at[w.ID] = max(fi.ModTime().Unix(), scripts[string(w.ID)+".sh"])
	}
	return at, nil
}

func record(d sysexec.Deps, st sites.State, ws []sites.WorkerState) (sites.State, error) {
	st.Workers = ws
	if err := sites.Save(d.Paths, st); err != nil {
		return st, fmt.Errorf("the workers run, but recording them in the state failed; run the command again: %w", err)
	}
	d.Logf("Site %s has %d workers", st.Name, len(ws))
	return st, nil
}

// resetFailed clears systemd's failed state, and with it the start limit's
// count, of each unit in blocks (systemctl show answers) that is failed, so
// a start or restart is not refused as too frequent. Only a failed unit is
// reset: reset-failed answers an error for a unit systemd hasn't loaded.
func resetFailed(ctx context.Context, d sysexec.Deps, blocks []map[string]string) error {
	var failed []string
	for _, b := range blocks {
		if b["ActiveState"] == "failed" {
			failed = append(failed, b["Id"])
		}
	}
	if len(failed) == 0 {
		return nil
	}
	d.Logf("Clearing the failed state of %d processes", len(failed))
	return systemd.Systemctl(ctx, d, append([]string{"reset-failed"}, failed...)...)
}

// RestartWorkers restarts every process of the site's workers, as its state
// lists them, by name: a glob would skip a stopped instance (spike). The
// restart is graceful (SIGTERM, then up to TimeoutStopSec, R04-D4) and
// queued (--no-block), so a deploy never waits out a long job; systemd
// carries it out. A site without workers makes no systemctl call. Deploys,
// rollbacks, .env writes and PHP switches use it (R04-D4, D9, D10, D15).
func RestartWorkers(ctx context.Context, d sysexec.Deps, st sites.State) error {
	units := instances(st.Name, st.Workers)
	if len(units) == 0 {
		return nil
	}
	blocks, err := show(ctx, d, "Id,ActiveState", units)
	if err != nil {
		return err
	}
	if err := resetFailed(ctx, d, blocks); err != nil {
		return err
	}
	d.Logf("Restarting the %d worker processes of site %s gracefully", len(units), st.Name)
	return systemd.Systemctl(ctx, d, append([]string{"restart", "--no-block"}, units...)...)
}
