package deploy

import (
	"context"
	"slices"
	"strings"
	"time"

	"zimam/agent/contract"
	"zimam/agent/internal/helper"
	"zimam/agent/internal/sysexec"
)

// releaseID names a release: <UTC yyyymmddhhmmss>-<first 7 hex of the
// commit> ("Data the agent keeps").
func releaseID(t time.Time, sha contract.CommitSHA) contract.ReleaseID {
	return contract.ReleaseID(t.UTC().Format("20060102150405") + "-" + string(sha[:7]))
}

// keptReleases is the set of release directory names a prune keeps: the
// newest keep releases the state lists, the live one and the one before it
// (which may be "initial", or an old release after a rollback). Anything
// else under site/releases goes, half-built releases the state never
// listed included.
func keptReleases(listed []contract.Release, keep contract.KeepReleases, live, previous string) map[string]bool {
	ids := make([]string, 0, len(listed))
	for _, r := range listed {
		ids = append(ids, string(r.ID))
	}
	// An id starts with its time, so the newest sort last.
	slices.SortFunc(ids, func(a, b string) int { return strings.Compare(b, a) })
	kept := map[string]bool{}
	for _, id := range ids[:min(len(ids), int(keep))] {
		kept[id] = true
	}
	for _, name := range []string{live, previous} {
		if name != "" {
			kept[name] = true
		}
	}
	return kept
}

// keepListed is the state's releases that the prune keeps, in their order.
func keepListed(listed []contract.Release, kept map[string]bool) []contract.Release {
	out := []contract.Release{}
	for _, r := range listed {
		if kept[string(r.ID)] {
			out = append(out, r)
		}
	}
	return out
}

// prune is step 11: everything under site/releases but the kept names.
func prune(ctx context.Context, d sysexec.Deps, h DeployHelper, a helper.Account, keep contract.KeepReleases, kept map[string]bool) error {
	d.Logf("Pruning releases beyond the %d kept, besides the live one and the one before it", keep)
	l, err := h.List(ctx, a, releasesDir)
	if err != nil {
		return err
	}
	for _, e := range l.Entries {
		if kept[e.Name] {
			continue
		}
		d.Logf("Removing release %s", e.Name)
		if err := h.RemoveTree(ctx, a, releasesDir+"/"+e.Name); err != nil {
			return err
		}
	}
	return nil
}
