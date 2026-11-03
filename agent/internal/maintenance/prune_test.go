package maintenance

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"zimam/agent/contract"
	"zimam/agent/internal/config"
	"zimam/agent/internal/paths"
	"zimam/agent/internal/sysexec"
)

func testConfig(t *testing.T, p paths.Paths) config.Agent {
	t.Helper()
	cfg := config.New("panel.example.com", "8.4", "8.4")
	if err := config.Save(p, cfg); err != nil {
		t.Fatal(err)
	}
	return cfg
}

const (
	jobOld     = "01JBX7Q2M5YQ8T3N6V0R4K9W1E"
	jobRecent  = "01JBX7Q2M5YQ8T3N6V0R4K9W1F"
	jobRunning = "01JBX7Q2M5YQ8T3N6V0R4K9W1G"
)

// Job logs and job state older than the retention period go; a job still
// queued or running stays, however old (§11.3).
func TestPruneJobLogsAndState(t *testing.T) {
	p := paths.Paths{Root: t.TempDir()}
	cfg := testConfig(t, p)
	cfg.LogRetentionDays = 3
	d := sysexec.Deps{Paths: p, Config: cfg, Logf: t.Logf, Now: func() time.Time { return capNow }}
	job := func(id, status string, age time.Duration) {
		dir := p.JobDir(contract.JobID(id))
		os.MkdirAll(dir, 0o700)
		os.WriteFile(p.JobState(contract.JobID(id)), []byte(`{"status":"`+status+`"}`), 0o600)
		os.MkdirAll(p.JobLogsDir(), 0o750)
		os.WriteFile(p.JobLog(contract.JobID(id)), []byte("[t] line\n"), 0o640)
		for _, path := range []string{p.JobState(contract.JobID(id)), dir, p.JobLog(contract.JobID(id))} {
			os.Chtimes(path, capNow.Add(-age), capNow.Add(-age))
		}
	}
	job(jobOld, "succeeded", 4*24*time.Hour)
	job(jobRecent, "failed", 2*24*time.Hour)
	job(jobRunning, "running", 10*24*time.Hour)
	// An orphan log, its state already gone, older than retention.
	os.WriteFile(filepath.Join(p.JobLogsDir(), "01JBX7Q2M5YQ8T3N6V0R4K9W1H.log"), nil, 0o640)
	os.Chtimes(filepath.Join(p.JobLogsDir(), "01JBX7Q2M5YQ8T3N6V0R4K9W1H.log"), capNow.Add(-5*24*time.Hour), capNow.Add(-5*24*time.Hour))
	// A file that isn't a job's is left alone.
	os.WriteFile(filepath.Join(p.JobLogsDir(), "notes.txt"), nil, 0o640)
	os.Chtimes(filepath.Join(p.JobLogsDir(), "notes.txt"), capNow.Add(-50*24*time.Hour), capNow.Add(-50*24*time.Hour))

	n, err := pruneJobs(d)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("pruned %d, want the old job and the orphan log", n)
	}
	gone := func(path string) bool { _, err := os.Lstat(path); return os.IsNotExist(err) }
	if !gone(p.JobDir(jobOld)) || !gone(p.JobLog(jobOld)) || !gone(filepath.Join(p.JobLogsDir(), "01JBX7Q2M5YQ8T3N6V0R4K9W1H.log")) {
		t.Error("the old job wasn't pruned")
	}
	for _, path := range []string{p.JobDir(jobRecent), p.JobLog(jobRecent), p.JobDir(jobRunning), p.JobLog(jobRunning), filepath.Join(p.JobLogsDir(), "notes.txt")} {
		if gone(path) {
			t.Errorf("%s was pruned", path)
		}
	}
}

// A run that pruned a job or deleted an archive writes one audit entry
// saying so (D18); a run that changed nothing writes none.
func TestMaintenanceAuditOnChange(t *testing.T) {
	e := newEnv(t)
	if err := e.maintain(); err != nil {
		t.Fatal(err)
	}
	if n := len(auditEntries(t, e.d.Paths)); n != 0 {
		t.Fatalf("a run that changed nothing wrote %d entries", n)
	}
	id := contract.JobID(jobOld)
	os.MkdirAll(e.d.Paths.JobDir(id), 0o700)
	os.WriteFile(e.d.Paths.JobState(id), []byte(`{"status":"succeeded"}`), 0o600)
	old := e.d.Now().Add(-30 * 24 * time.Hour)
	os.Chtimes(e.d.Paths.JobState(id), old, old)
	if err := e.maintain(); err != nil {
		t.Fatal(err)
	}
	entries := auditEntries(t, e.d.Paths)
	if len(entries) != 1 {
		t.Fatalf("%d entries, want 1", len(entries))
	}
	params, _ := entries[0]["params"].(map[string]any)
	if entries[0]["command"] != "maintenance" || entries[0]["outcome"] != "ok" || params["pruned_jobs"] != float64(1) {
		t.Errorf("entry %v", entries[0])
	}
}
