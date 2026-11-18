package siteprocs

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"zimam/agent/contract"
	"zimam/agent/internal/sites"
	"zimam/agent/internal/sysexec"
	"zimam/agent/internal/systemd"
)

const systemctl = "/usr/bin/systemctl"

// statusProps are what the status read asks systemd about each instance.
const statusProps = "Id,ActiveState,SubState,NRestarts,StateChangeTimestamp,Result,ExecMainStatus"

// show runs one systemctl show for units, with timestamps in Unix seconds,
// and returns each unit's properties in the order asked. Each block must
// name the unit asked for, so one unit's state is never pinned on another.
func show(ctx context.Context, d sysexec.Deps, props string, units []string) ([]map[string]string, error) {
	res, err := d.Run.Run(ctx, sysexec.Cmd{Path: systemctl, Args: append([]string{"show", "--timestamp=unix", "-p", props, "--"}, units...)})
	if err != nil {
		return nil, err
	}
	var blocks []map[string]string
	cur := map[string]string{}
	for line := range strings.Lines(string(res.Stdout) + "\n") {
		line = strings.TrimRight(line, "\n")
		if line == "" {
			if len(cur) > 0 {
				blocks = append(blocks, cur)
				cur = map[string]string{}
			}
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			return nil, fmt.Errorf("systemctl show printed %q", line)
		}
		cur[k] = v
	}
	if len(blocks) != len(units) {
		return nil, fmt.Errorf("systemctl show answered for %d units, want %d", len(blocks), len(units))
	}
	for i, b := range blocks {
		if b["Id"] != units[i] {
			return nil, fmt.Errorf("systemctl show answered %q where %s was asked for", b["Id"], units[i])
		}
	}
	return blocks, nil
}

// failing is the spike's rule (R04-D8): a crash loop never reaches failed on
// its own, and NRestarts also counts clean exits (queue:restart, --max-time),
// so an instance is failing when it waits to restart after an exit that
// failed. systemd's own failed (the start limit) is failing too.
func failing(b map[string]string) bool {
	return b["ActiveState"] == "failed" ||
		b["SubState"] == "auto-restart" && (b["Result"] == "exit-code" || cmp.Or(b["ExecMainStatus"], "0") != "0")
}

// since turns systemd's "@<seconds>" into RFC 3339, or "" when there is none.
func since(v string) string {
	n := unixSeconds(v)
	if n == 0 {
		return ""
	}
	return time.Unix(n, 0).UTC().Format(time.RFC3339)
}

// instances are the unit names of every process of the workers, in order.
func instances(site contract.SiteName, ws []sites.WorkerState) []string {
	var names []string
	for _, w := range ws {
		names = append(names, systemd.WorkerInstances(site, w.ID, 1, w.Processes)...)
	}
	return names
}

// sortedWorkers is the state's workers in id order.
func sortedWorkers(ws []sites.WorkerState) []sites.WorkerState {
	ws = slices.Clone(ws)
	slices.SortFunc(ws, func(a, b sites.WorkerState) int { return strings.Compare(string(a.ID), string(b.ID)) })
	return ws
}

// Status is site.workers.status: each worker of the site's state with each
// of its processes as systemd reports it, from one systemctl show.
func Status(ctx context.Context, d sysexec.Deps, st sites.State) (contract.SiteWorkersStatusResult, error) {
	res := contract.NewSiteWorkersStatusResult(st.Name)
	ws := sortedWorkers(st.Workers)
	if len(ws) == 0 {
		return res, nil
	}
	blocks, err := show(ctx, d, statusProps, instances(st.Name, ws))
	if err != nil {
		return res, err
	}
	for _, w := range ws {
		s := contract.WorkerStatus{ID: w.ID, Instances: []contract.WorkerInstanceStatus{}}
		for n := 1; n <= w.Processes; n++ {
			b := blocks[0]
			blocks = blocks[1:]
			restarts, _ := strconv.Atoi(b["NRestarts"])
			s.Instances = append(s.Instances, contract.WorkerInstanceStatus{
				Instance: n, Active: b["ActiveState"], Sub: b["SubState"], Restarts: restarts,
				Since: since(b["StateChangeTimestamp"]), Failing: failing(b),
			})
		}
		res.Workers = append(res.Workers, s)
	}
	return res, nil
}

// Metrics is what system.metrics reports of every site's workers (R04-D8,
// D17): per worker, its processes' restarts summed, whether one is failing
// and whether systemd gave one up. The panel keeps the samples, so it warns
// on worker-crashloop:<site>:<id> when restarts rose by more than 5 in 10
// minutes while the worker is failing. One systemctl show covers every
// site; a server without workers makes no call.
func Metrics(ctx context.Context, d sysexec.Deps) ([]contract.WorkerMetric, error) {
	all, err := sites.All(d.Paths)
	if err != nil {
		return nil, err
	}
	var units []string
	for _, st := range all {
		units = append(units, instances(st.Name, sortedWorkers(st.Workers))...)
	}
	if len(units) == 0 {
		return nil, nil
	}
	blocks, err := show(ctx, d, statusProps, units)
	if err != nil {
		return nil, err
	}
	var out []contract.WorkerMetric
	for _, st := range all {
		for _, w := range sortedWorkers(st.Workers) {
			m := contract.WorkerMetric{Site: st.Name, Worker: w.ID}
			for range w.Processes {
				b := blocks[0]
				blocks = blocks[1:]
				n, _ := strconv.Atoi(b["NRestarts"])
				m.Restarts += n
				m.Failing = m.Failing || failing(b)
				m.Failed = m.Failed || b["ActiveState"] == "failed"
			}
			out = append(out, m)
		}
	}
	return out, nil
}

// unixSeconds reads systemd's "@<seconds>", or 0 when there is none.
func unixSeconds(v string) int64 {
	n, err := strconv.ParseInt(strings.TrimPrefix(v, "@"), 10, 64)
	if err != nil || n < 0 {
		return 0
	}
	return n
}
