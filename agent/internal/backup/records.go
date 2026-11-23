package backup

import (
	"errors"
	"io/fs"
	"os"
	"slices"
	"strings"

	"zimam/agent/contract"
	"zimam/agent/internal/paths"
)

// saveRecord writes a run's record (R05-D9).
func saveRecord(p paths.Paths, r contract.BackupRunRecord) error {
	return saveJSON(p, p.BackupRunRecord(r.Plan, r.Backup), r)
}

// planRecords reads a plan's records, unsorted. A plan without any has
// none.
func planRecords(p paths.Paths, plan contract.PlanID) ([]contract.BackupRunRecord, error) {
	entries, err := os.ReadDir(p.BackupRunsDir(plan))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []contract.BackupRunRecord
	for _, e := range entries {
		id, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok || !e.Type().IsRegular() || contract.BackupID(id).Validate() != nil {
			continue // a temp file being written, or not a record
		}
		var r contract.BackupRunRecord
		if err := loadJSON(p.BackupRunRecord(plan, contract.BackupID(id)), &r); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}

// Runs answers backup.runs: one plan's records, or every plan's, after
// since when given, oldest first (R05-D9). The panel polls it and turns new
// records into notifications.
func Runs(p paths.Paths, plan contract.PlanID, since contract.BackupID) (contract.BackupRunsResult, error) {
	res := contract.BackupRunsResult{Runs: []contract.BackupRunRecord{}}
	plans := []contract.PlanID{plan}
	if plan == "" {
		entries, err := os.ReadDir(p.BackupStateDir())
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return res, err
		}
		plans = nil
		for _, e := range entries {
			if e.IsDir() && contract.PlanID(e.Name()).Validate() == nil {
				plans = append(plans, contract.PlanID(e.Name()))
			}
		}
	}
	for _, pl := range plans {
		rs, err := planRecords(p, pl)
		if err != nil {
			return res, err
		}
		for _, r := range rs {
			if r.Backup > since {
				res.Runs = append(res.Runs, r)
			}
		}
	}
	slices.SortFunc(res.Runs, func(a, b contract.BackupRunRecord) int {
		if c := strings.Compare(string(a.Backup), string(b.Backup)); c != 0 {
			return c
		}
		return strings.Compare(string(a.Plan), string(b.Plan))
	})
	return res, nil
}
