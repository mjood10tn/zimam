package backup

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"zimam/agent/contract"
	"zimam/agent/internal/rclone"
	"zimam/agent/internal/release"
	"zimam/agent/internal/sysexec"
)

// manifestName is the object that makes a backup complete: uploaded last,
// it lists every part (R05-D5). A backup without it is never offered.
const manifestName = "manifest.json"

// Manifest is a complete backup's record on the destination.
type Manifest struct {
	Plan contract.PlanID `json:"plan"`
	contract.BackupInfo
	AgentVersion  string `json:"agent_version"`
	RcloneVersion string `json:"rclone_version"`
}

// backupDir is a backup's folder on its destination's crypt remote.
func backupDir(dest contract.DestinationID, plan contract.PlanID, id contract.BackupID) string {
	return string(dest) + ":" + string(plan) + "/" + string(id) + "/"
}

// WriteManifest uploads the manifest of a backup whose parts all passed.
func WriteManifest(ctx context.Context, d sysexec.Deps, dest contract.DestinationID, plan contract.PlanID, id contract.BackupID, started time.Time, parts []contract.BackupPartInfo) error {
	m := Manifest{Plan: plan, BackupInfo: contract.BackupInfo{Backup: id, StartedAt: started, FinishedAt: d.Now().UTC(), Parts: parts},
		AgentVersion: release.Version, RcloneVersion: rclone.Version}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	if _, err := rcloneRun(ctx, d, strings.NewReader(string(data)+"\n"), "rcat", backupDir(dest, plan, id)+manifestName); err != nil {
		return fmt.Errorf("uploading the manifest: %w", err)
	}
	return nil
}
