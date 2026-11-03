package commands_test

import (
	"context"
	"os"
	"strings"
	"testing"

	"zimam/agent/contract"
	"zimam/agent/internal/commands"
	"zimam/agent/internal/config"
	"zimam/agent/internal/jobs"
	"zimam/agent/internal/sites"
	"zimam/agent/internal/sysexec"
)

// logs.policy stores the three values in agent.json, re-renders every
// rotation rule and the journald drop-in, and restarts journald (§11.4).
func TestPolicyStoresAndRerenders(t *testing.T) {
	d, ok := contract.Lookup("logs.policy")
	if !ok || d.Kind != contract.KindJob || !d.Audited || d.Subject(&contract.LogsPolicyParams{}) != "server" {
		t.Fatalf("logs.policy = %+v, %v", d, ok)
	}
	if l, ok := jobs.LimitsFor("logs.policy"); !ok || l != (jobs.Limits{MemoryMax: "256M", RuntimeMaxSec: 120}) {
		t.Errorf("limits %+v", l)
	}
	e := newPHPEnv(t)
	if err := sites.Save(e.p, sites.State{Name: "shop", Domain: "shop.example.com", Type: contract.SiteStatic, TemplateVersion: 1, Security: sites.DefaultSecurity()}); err != nil {
		t.Fatal(err)
	}
	e.run.Add(sysexec.Script{Path: "/usr/sbin/logrotate", Args: []string{"-d", "--state", e.p.LogrotateState(), e.p.LogrotateConf()}})
	e.run.Add(sysexec.Script{Path: "/usr/bin/systemctl", Args: []string{"restart", "systemd-journald"}})
	cfg, err := config.Load(e.p)
	if err != nil {
		t.Fatal(err)
	}
	h := commands.JobHandlers()["logs.policy"]
	if h == nil {
		t.Fatal("logs.policy has no job handler")
	}
	j := &jobs.Context{ID: "01JBX7Q3A1B2C3D4E5F6G7H8J9", Logf: t.Logf, Config: cfg, Paths: e.p, Run: e.run}
	p := contract.LogsPolicyParams{RetentionDays: 30, AuditRetentionDays: 730, SiteCapBytes: contract.SiteCapBytes(2 * contract.GiB)}
	res, err := h(context.Background(), j, &p)
	if err != nil {
		t.Fatal(err)
	}
	if res != contract.LogsPolicyResult(p) {
		t.Errorf("result %+v", res)
	}
	stored, err := config.Load(e.p)
	if err != nil {
		t.Fatal(err)
	}
	if stored.LogRetentionDays != 30 || stored.AuditRetentionDays != 730 || int64(stored.SiteLogCapBytes) != 2*contract.GiB {
		t.Errorf("agent.json = %+v", stored)
	}
	for path, want := range map[string]string{
		e.p.SiteLogrotateConf("shop"): "maxage 30",
		e.p.OwnLogrotateConf():        "maxage 730",
		e.p.JournaldDropIn():          "MaxRetentionSec=30day",
	} {
		b, err := os.ReadFile(path)
		if err != nil || !strings.Contains(string(b), want) {
			t.Errorf("%s = %q, %v; want %q", path, b, err, want)
		}
	}
	if calls := e.calls(); len(calls) == 0 || calls[len(calls)-1] != "/usr/bin/systemctl restart systemd-journald" {
		t.Errorf("ran %v", calls)
	}
}
