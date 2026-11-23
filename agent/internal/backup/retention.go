package backup

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"zimam/agent/contract"
	"zimam/agent/internal/sysexec"
)

// staleIncomplete is how old a backup without its manifest must be before
// retention deletes it: a younger one may still be uploading (R05-D8).
const staleIncomplete = 24 * time.Hour

const idLayout = "20060102T150405Z"

// listBackups lists a plan's folder on its destination: each backup id
// found, and whether its manifest is there. Names that aren't backup ids
// are left out, so nothing else is ever touched. A folder that doesn't
// exist yet (rclone's exit 3) holds none.
func listBackups(ctx context.Context, d sysexec.Deps, dest contract.DestinationID, plan contract.PlanID) (map[contract.BackupID]bool, error) {
	res, err := rcloneRun(ctx, d, nil, "lsf", "--max-depth", "2", string(dest)+":"+string(plan))
	var exit *sysexec.ExitError
	if errors.As(err, &exit) && exit.Code == 3 {
		return map[contract.BackupID]bool{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("listing the backups of %s: %w", plan, err)
	}
	out := map[contract.BackupID]bool{}
	for line := range strings.SplitSeq(strings.TrimSpace(string(res.Stdout)), "\n") {
		dir, rest, _ := strings.Cut(line, "/")
		id := contract.BackupID(dir)
		if id.Validate() != nil {
			continue
		}
		out[id] = out[id] || rest == manifestName
	}
	return out, nil
}

// keep is what the retention rules keep among complete backups (R05-D8):
// the newest keep_last, and the newest backup of each of the newest
// keep_daily days, keep_weekly ISO weeks and keep_monthly months that have
// one. Periods are in UTC, the ids' zone.
func keep(complete []contract.BackupID, r contract.Retention) map[contract.BackupID]bool {
	ids := slices.Clone(complete)
	slices.Sort(ids)
	slices.Reverse(ids)
	kept := map[contract.BackupID]bool{}
	for i, id := range ids {
		if i < r.KeepLast {
			kept[id] = true
		}
	}
	rule := func(n int, period func(time.Time) string) {
		seen := map[string]bool{}
		for _, id := range ids {
			t, err := time.Parse(idLayout, string(id))
			if err != nil || len(seen) >= n {
				continue
			}
			if p := period(t); !seen[p] {
				seen[p] = true
				kept[id] = true
			}
		}
	}
	rule(r.KeepDaily, func(t time.Time) string { return t.Format("2006-01-02") })
	rule(r.KeepWeekly, func(t time.Time) string { y, w := t.ISOWeek(); return fmt.Sprint(y, "-", w) })
	rule(r.KeepMonthly, func(t time.Time) string { return t.Format("2006-01") })
	return kept
}

// Prune deletes the plan's backups no rule keeps, and incomplete ones older
// than a day, with rclone purge on <dest>:<plan>/<id> for valid ids only
// (R05-D8). It runs after a successful run, under the plan's lock.
func Prune(ctx context.Context, d sysexec.Deps, dest contract.DestinationID, plan contract.PlanID, r contract.Retention) error {
	found, err := listBackups(ctx, d, dest, plan)
	if err != nil {
		return err
	}
	var complete, doomed []contract.BackupID
	for id, ok := range found {
		if ok {
			complete = append(complete, id)
			continue
		}
		// An id that matches but isn't a real time has no age: it stays.
		if t, err := time.Parse(idLayout, string(id)); err == nil && d.Now().Sub(t) > staleIncomplete {
			doomed = append(doomed, id)
		}
	}
	kept := keep(complete, r)
	for _, id := range complete {
		if !kept[id] {
			doomed = append(doomed, id)
		}
	}
	slices.Sort(doomed)
	slices.Reverse(doomed)
	var errs []error
	for _, id := range doomed {
		if _, err := rcloneRun(ctx, d, nil, "purge", string(dest)+":"+string(plan)+"/"+string(id)); err != nil {
			errs = append(errs, fmt.Errorf("deleting backup %s: %w", id, err))
			continue
		}
		d.Logf("Deleted backup %s of %s", id, plan)
	}
	return errors.Join(errs...)
}
