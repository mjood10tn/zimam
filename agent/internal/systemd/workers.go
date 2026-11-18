package systemd

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"slices"
	"strconv"
	"strings"

	"zimam/agent/contract"
	"zimam/agent/internal/configtx"
	"zimam/agent/internal/sysexec"
)

// A site's worker units (R04-D2, ruling R4): one template unit per worker,
// zimam-worker-<site>_<id>@.service, run as instances @1..@<processes>, all
// PartOf the site's zimam-site-<site>.target. A site name has no "_" and a
// worker id neither, so one site's names never match another's.

// WorkerUnit is a worker's template unit.
func WorkerUnit(site contract.SiteName, id contract.WorkerID) string {
	return "zimam-worker-" + string(site) + "_" + string(id) + "@.service"
}

// WorkerInstance is process n of a worker.
func WorkerInstance(site contract.SiteName, id contract.WorkerID, n int) string {
	return "zimam-worker-" + string(site) + "_" + string(id) + "@" + strconv.Itoa(n) + ".service"
}

// WorkerInstances are processes from..to of a worker.
func WorkerInstances(site contract.SiteName, id contract.WorkerID, from, to int) []string {
	var names []string
	for n := from; n <= to; n++ {
		names = append(names, WorkerInstance(site, id, n))
	}
	return names
}

// SiteTarget groups a site's workers.
func SiteTarget(site contract.SiteName) string { return "zimam-site-" + string(site) + ".target" }

// WorkerIDs are the workers whose template unit is on disk, sorted: what is
// really there, whatever the state says, so a run that failed half way
// leaves nothing behind for the next one.
func WorkerIDs(d sysexec.Deps, site contract.SiteName) ([]contract.WorkerID, error) {
	entries, err := os.ReadDir(d.Paths.SystemdUnitDir())
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var ids []contract.WorkerID
	for _, e := range entries {
		rest, ok := strings.CutPrefix(e.Name(), "zimam-worker-"+string(site)+"_")
		id, ok2 := strings.CutSuffix(rest, "@.service")
		if ok && ok2 && contract.WorkerID(id).Validate() == nil {
			ids = append(ids, contract.WorkerID(id))
		}
	}
	slices.Sort(ids)
	return ids, nil
}

// Systemctl runs systemctl with args.
func Systemctl(ctx context.Context, d sysexec.Deps, args ...string) error {
	_, err := d.Run.Run(ctx, sysexec.Cmd{Path: systemctl, Args: args})
	return err
}

// DisableWorkers stops and disables every process a worker can have
// (@1..@10) of each worker in ids, whose unit files must still be there.
// --no-block: a graceful stop may take a job's whole timeout, and the
// command must not wait for it.
func DisableWorkers(ctx context.Context, d sysexec.Deps, site contract.SiteName, ids []contract.WorkerID) error {
	if len(ids) == 0 {
		return nil
	}
	args := []string{"disable", "--now", "--no-block"}
	for _, id := range ids {
		args = append(args, WorkerInstances(site, id, 1, contract.MaxWorkerProcesses)...)
	}
	d.Logf("Stopping and disabling the workers %s of site %s", joinIDs(ids), site)
	return Systemctl(ctx, d, args...)
}

// RemoveSiteUnits stops, disables and deletes every worker unit of the site
// and its target, then reloads systemd. site.destroy runs it before it
// kills the site user's processes and deletes the user: a unit whose user is
// gone fails to start (spike). A site without units makes no systemctl call.
func RemoveSiteUnits(ctx context.Context, d sysexec.Deps, site contract.SiteName) error {
	ids, err := WorkerIDs(d, site)
	if err != nil {
		return err
	}
	target := d.Paths.SystemdUnit(SiteTarget(site))
	_, terr := os.Lstat(target)
	hasTarget := terr == nil
	if len(ids) == 0 && !hasTarget {
		return nil
	}
	if hasTarget {
		if err := Systemctl(ctx, d, "disable", "--now", "--no-block", SiteTarget(site)); err != nil {
			return err
		}
	}
	if err := DisableWorkers(ctx, d, site, ids); err != nil {
		return err
	}
	changes := []configtx.Change{{Path: target, Remove: true}}
	for _, id := range ids {
		changes = append(changes, configtx.Change{Path: d.Paths.SystemdUnit(WorkerUnit(site, id)), Remove: true})
	}
	d.Logf("Deleting the worker units of site %s", site)
	if err := configtx.Apply(ctx, changes, func(context.Context) error { return nil }); err != nil {
		return err
	}
	// disable empties the target's wants directory; an empty one is left.
	if err := os.Remove(d.Paths.SystemdUnit(SiteTarget(site) + ".wants")); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("removing the target's wants directory: %w", err)
	}
	return Systemctl(ctx, d, "daemon-reload")
}

func joinIDs(ids []contract.WorkerID) string {
	s := make([]string, len(ids))
	for i, id := range ids {
		s[i] = string(id)
	}
	return strings.Join(s, ", ")
}
