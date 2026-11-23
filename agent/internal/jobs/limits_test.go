package jobs

import (
	"context"
	"strings"
	"testing"

	"zimam/agent/contract"
	"zimam/agent/internal/sysexec"
)

// The §5.7 table.
func TestLimitsMatchDesign(t *testing.T) {
	want := map[string]Limits{
		"php.install":    {MemoryMax: "1G", RuntimeMaxSec: 900},
		"php.uninstall":  {MemoryMax: "1G", RuntimeMaxSec: 900},
		"site.provision": {MemoryMax: "512M", RuntimeMaxSec: 300},
		"site.php":       {MemoryMax: "512M", RuntimeMaxSec: 300},
		"site.security":  {MemoryMax: "512M", RuntimeMaxSec: 300},
		"site.destroy":   {MemoryMax: "512M", RuntimeMaxSec: 300},
		"cert.issue":     {MemoryMax: "512M", RuntimeMaxSec: 300},
		"security.check": {MemoryMax: "512M", RuntimeMaxSec: 600},
		"php.default":    {MemoryMax: "256M", RuntimeMaxSec: 120},
		"logs.policy":    {MemoryMax: "256M", RuntimeMaxSec: 120},
		// v0.2.0's job commands (the plan's "New agent commands" table).
		"site.repo":      {MemoryMax: "512M", RuntimeMaxSec: 600},
		"site.deploy":    {MemoryMax: "2G", RuntimeMaxSec: 1800},
		"site.rollback":  {MemoryMax: "512M", RuntimeMaxSec: 300},
		"site.env.write": {MemoryMax: "256M", RuntimeMaxSec: 120},
		// v0.3.0 database and cache commands.
		"mysql.install":  {MemoryMax: "1G", RuntimeMaxSec: 900},
		"db.create":      {MemoryMax: "256M", RuntimeMaxSec: 120},
		"db.drop":        {MemoryMax: "256M", RuntimeMaxSec: 300},
		"db.user":        {MemoryMax: "256M", RuntimeMaxSec: 120},
		"valkey.install": {MemoryMax: "512M", RuntimeMaxSec: 900},
		"cache.user":     {MemoryMax: "256M", RuntimeMaxSec: 120},
		// v0.4.0 worker, cron and Node commands (the plan's "New agent commands" table).
		"site.workers":         {MemoryMax: "256M", RuntimeMaxSec: 300},
		"site.workers.restart": {MemoryMax: "128M", RuntimeMaxSec: 300},
		"site.cron":            {MemoryMax: "128M", RuntimeMaxSec: 120},
		"node.install":         {MemoryMax: "1G", RuntimeMaxSec: 900},
		"node.uninstall":       {MemoryMax: "512M", RuntimeMaxSec: 600},
		// v0.5.0 backup commands (the plan's "New agent commands" table; OQ5).
		"backup.destination":        {MemoryMax: "256M", RuntimeMaxSec: 300},
		"backup.destination.delete": {MemoryMax: "128M", RuntimeMaxSec: 120},
		"backup.destination.test":   {MemoryMax: "256M", RuntimeMaxSec: 300},
		"backup.plan":               {MemoryMax: "128M", RuntimeMaxSec: 120},
		"backup.plan.delete":        {MemoryMax: "128M", RuntimeMaxSec: 120},
		"backup.run":                {MemoryMax: "1G", RuntimeMaxSec: 21600},
		"backup.restore":            {MemoryMax: "1G", RuntimeMaxSec: 21600},
	}
	for name, w := range want {
		if got, ok := LimitsFor(name); !ok || got != w {
			t.Errorf("LimitsFor(%s) = %+v, %t; want %+v", name, got, ok, w)
		}
	}
	for name := range limits {
		// e2e builds add their test-only commands (D22).
		if _, ok := want[name]; !ok && !strings.HasPrefix(name, "e2e.") {
			t.Errorf("%s has limits but isn't in §5.7 or a release plan", name)
		}
	}
}

// A job command without limits is a programming error.
func TestRegisteredJobCommandsHaveLimits(t *testing.T) {
	for _, d := range contract.All() {
		if _, ok := LimitsFor(d.Name); d.Kind == contract.KindJob && !ok {
			t.Errorf("job command %s has no limits", d.Name)
		}
	}
}

func TestSystemctl(t *testing.T) {
	const unit = "zimam-job-01JBX7Q3A1B2C3D4E5F6G7H8J9"
	run := sysexec.NewFakeRunner(
		sysexec.Script{Path: "/usr/bin/systemd-run", Args: []string{
			"--unit=" + unit, "--property=Type=exec", "--property=MemoryMax=1G", "--no-block", "--",
			"/usr/local/sbin/zimam-agent", "job", "run", "01JBX7Q3A1B2C3D4E5F6G7H8J9",
		}},
		sysexec.Script{
			Path: "/usr/bin/systemctl", Args: []string{"show", "--property=LoadState,ActiveState,Result", unit},
			Result: sysexec.Result{Stdout: []byte("LoadState=loaded\nActiveState=failed\nResult=oom-kill\n")},
		},
		sysexec.Script{Path: "/usr/bin/systemctl", Args: []string{"stop", unit}},
		sysexec.Script{Path: "/usr/bin/systemctl", Args: []string{"reset-failed", unit}},
	)
	s := Systemctl{Run: run}
	ctx := context.Background()
	argv := []string{"/usr/local/sbin/zimam-agent", "job", "run", "01JBX7Q3A1B2C3D4E5F6G7H8J9"}
	if err := s.StartTransient(ctx, unit, []string{"Type=exec", "MemoryMax=1G"}, argv); err != nil {
		t.Errorf("StartTransient: %v", err)
	}
	if u, err := s.Show(ctx, unit); err != nil || u != (UnitState{LoadState: "loaded", ActiveState: "failed", Result: "oom-kill"}) {
		t.Errorf("Show = %+v, %v", u, err)
	}
	if err := s.Stop(ctx, unit); err != nil {
		t.Errorf("Stop: %v", err)
	}
	if err := s.ResetFailed(ctx, unit); err != nil {
		t.Errorf("ResetFailed: %v", err)
	}
	if n := len(run.Calls()); n != 4 {
		t.Errorf("%d calls, want 4", n)
	}
}
