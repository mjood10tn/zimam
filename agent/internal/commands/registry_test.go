package commands

import (
	"testing"

	"zimam/agent/internal/jobs"
	"zimam/agent/internal/sysexec"
)

func TestNewAddsJobCommands(t *testing.T) {
	if h := New(sysexec.Deps{}, nil); len(h) != 13 || h["change.confirm"] == nil || h["backup.destination.keys"] == nil || h["backup.runs"] == nil || h["backup.list"] == nil || h["site.workers.status"] == nil || h["agent.version"] == nil || h["system.status"] == nil || h["system.metrics"] == nil || h["logs.list"] == nil || h["logs.read"] == nil || h["site.releases"] == nil || h["site.env.read"] == nil || h["db.list"] == nil {
		t.Errorf("New(d, nil) has %d handlers, want only the reads and change.confirm: agent.version, system.status, system.metrics, logs.list, logs.read, site.releases, site.env.read, db.list, site.workers.status, backup.destination.keys, backup.runs and backup.list", len(h))
	}
	m := &jobs.Manager{Handlers: map[string]jobs.Handler{"php.default": nil}}
	h := New(sysexec.Deps{}, m)
	for _, name := range []string{"agent.version", "system.status", "system.metrics", "site.releases", "site.env.read", "db.list", "job.status", "job.follow", "job.cancel", "php.default"} {
		if h[name] == nil {
			t.Errorf("New(d, m) has no handler for %s", name)
		}
	}
}

func TestMySQLInstallHasAHandlerAndNoPrecheck(t *testing.T) {
	if JobHandlers()["mysql.install"] == nil {
		t.Error("JobHandlers has no mysql.install")
	}
	if _, ok := Prechecks(sysexec.Deps{})["mysql.install"]; ok {
		t.Error("mysql.install has a precheck: running it again must converge, not conflict")
	}
}

func TestValkeyInstallHasAHandlerAndNoPrecheck(t *testing.T) {
	if JobHandlers()["valkey.install"] == nil {
		t.Error("JobHandlers has no valkey.install")
	}
	if _, ok := Prechecks(sysexec.Deps{})["valkey.install"]; ok {
		t.Error("valkey.install has a precheck: running it again must converge, not conflict")
	}
}
