package backup

import (
	"zimam/agent/contract"
	"zimam/agent/internal/paths"
)

// plansFile is the plans' state: each plan as backup.plan set it.
type plansFile struct {
	Plans []contract.BackupPlanParams `json:"plans"`
}

// loadPlans reads the plans strictly. A missing file is none.
func loadPlans(p paths.Paths) ([]contract.BackupPlanParams, error) {
	var f plansFile
	if err := loadJSON(p.BackupPlansState(), &f); err != nil {
		return nil, err
	}
	return f.Plans, nil
}
