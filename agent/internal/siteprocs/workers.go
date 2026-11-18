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
//  2. stop and disable the unwanted ones and, for a worker whose processes
//     shrank, its surplus instances;
//  3. write the changed unit files and delete the unwanted, tested with
//     systemd-analyze verify --recursive-errors=no (plain verify passes a
//     bad directive, spike); a failed test restores the previous files;
//  4. write the scripts, deleting the unwanted ones;
//  5. one daemon-reload;
//  6. restart, gracefully, each kept worker whose unit or script changed,
//     and enable and start every wanted instance by name, which leaves a
//     running unchanged one alone;
//  7. record the workers, without their commands, in the state (R04-D16).
//
// Removals are found on disk, not in the state, so a run that failed half
// way is finished by running it again.
func Reconcile(ctx context.Context, d sysexec.Deps, h Helper, a helper.Account, st sites.State, workers []contract.WorkerSpec) (sites.State, error) {
	site := st.Name
	if len(workers) == 0 {
		if err := systemd.RemoveSiteUnits(ctx, d, site); err != nil {
			return st, err
		}
		if _, err := WriteWorkerScripts(ctx, h, a, st, nil); err != nil {
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
	isNew, unitChanged := map[contract.WorkerID]bool{}, map[contract.WorkerID]bool{}
	want := func(path string, content []byte) (fresh, changed bool, err error) {
		verify = append(verify, path)
		old, err := os.ReadFile(path)
		switch {
		case errors.Is(err, fs.ErrNotExist):
			fresh = true
		case err != nil:
			return false, false, err
		}
		if fresh || !bytes.Equal(old, content) {
			changes = append(changes, configtx.RootFile(path, content, 0o644))
			return fresh, true, nil
		}
		return false, false, nil
	}
	target, err := templates.Render("systemd/zimam-site.target", templates.SiteTarget{Site: site})
	if err != nil {
		return st, err
	}
	if _, _, err := want(d.Paths.SystemdUnit(systemd.SiteTarget(site)), target); err != nil {
		return st, err
	}
	wanted := map[contract.WorkerID]bool{}
	for _, w := range workers {
		wanted[w.ID] = true
		unit, err := renderUnit(site, w)
		if err != nil {
			return st, fmt.Errorf("worker %s's unit: %w", w.ID, err)
		}
		if isNew[w.ID], unitChanged[w.ID], err = want(d.Paths.SystemdUnit(systemd.WorkerUnit(site, w.ID)), unit); err != nil {
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
	var surplus []string
	for _, w := range workers {
		for _, o := range st.Workers {
			if o.ID == w.ID && o.Processes > w.Processes && slices.Contains(onDisk, w.ID) {
				surplus = append(surplus, systemd.WorkerInstances(site, w.ID, w.Processes+1, o.Processes)...)
			}
		}
	}
	if len(surplus) > 0 {
		d.Logf("Stopping %d processes no longer wanted", len(surplus))
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
	scriptChanged, err := WriteWorkerScripts(ctx, h, a, st, workers)
	if err != nil {
		return st, err
	}

	// Step 5.
	if len(changes) > 0 {
		if err := systemd.Systemctl(ctx, d, "daemon-reload"); err != nil {
			return st, err
		}
	}

	// Step 6.
	var states []sites.WorkerState
	var all, restart, start []string
	for _, w := range workers {
		states = append(states, sites.NewWorkerState(w))
		names := systemd.WorkerInstances(site, w.ID, 1, w.Processes)
		all = append(all, names...)
		if !isNew[w.ID] && (unitChanged[w.ID] || scriptChanged[w.ID]) {
			restart = append(restart, names...)
		} else {
			start = append(start, names...)
		}
	}
	if err := resetFailed(ctx, d, all); err != nil {
		return st, err
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
	if err := systemd.Systemctl(ctx, d, append([]string{"start", systemd.SiteTarget(site)}, start...)...); err != nil {
		return st, err
	}

	// Step 7.
	return record(d, st, states)
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
// count, of each of units that is failed, so a start or restart is not
// refused as too frequent. Only a failed unit is reset: reset-failed answers
// an error for a unit systemd hasn't loaded.
func resetFailed(ctx context.Context, d sysexec.Deps, units []string) error {
	blocks, err := show(ctx, d, "Id,ActiveState", units)
	if err != nil {
		return err
	}
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
	if err := resetFailed(ctx, d, units); err != nil {
		return err
	}
	d.Logf("Restarting the %d worker processes of site %s gracefully", len(units), st.Name)
	return systemd.Systemctl(ctx, d, append([]string{"restart", "--no-block"}, units...)...)
}
