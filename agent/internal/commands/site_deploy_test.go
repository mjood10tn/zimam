package commands_test

import (
	"context"
	"testing"

	"zimam/agent/contract"
	"zimam/agent/internal/commands"
	"zimam/agent/internal/jobs"
	"zimam/agent/internal/sites"
)

func TestSiteDeployWired(t *testing.T) {
	d, ok := contract.Lookup("site.deploy")
	if !ok || d.Kind != contract.KindJob || !d.Audited {
		t.Fatalf("site.deploy = %+v, %v; want an audited job", d, ok)
	}
	if got := d.Subject(&contract.SiteDeployParams{Name: "shop"}); got != "site:shop" {
		t.Errorf("subject %q, want site:shop", got)
	}
	if l, ok := jobs.LimitsFor("site.deploy"); !ok || l != (jobs.Limits{MemoryMax: "2G", RuntimeMaxSec: 1800}) {
		t.Errorf("limits %+v", l)
	}
	if commands.JobHandlers()["site.deploy"] == nil {
		t.Error("site.deploy has no job handler")
	}
	e := newPHPEnv(t)
	pre := commands.Prechecks(e.d)["site.deploy"]
	if pre == nil {
		t.Fatal("site.deploy has no precheck")
	}
	params := &contract.SiteDeployParams{Name: "shop", Trigger: contract.DeployManual}
	wantCode(t, pre(context.Background(), params), contract.ErrNotFound)
}

// TestDeployOfSiteWithoutRepoIsConflict: a site that never had site.repo
// can't deploy; the precheck says so before a job starts.
func TestDeployOfSiteWithoutRepoIsConflict(t *testing.T) {
	e := newPHPEnv(t)
	if err := sites.Save(e.p, sites.State{Name: "shop", Domain: "shop.example.com", Type: contract.SiteStatic, TemplateVersion: 1, Security: sites.DefaultSecurity()}); err != nil {
		t.Fatal(err)
	}
	pre := commands.Prechecks(e.d)["site.deploy"]
	if pre == nil {
		t.Fatal("site.deploy has no precheck")
	}
	wantCode(t, pre(context.Background(), &contract.SiteDeployParams{Name: "shop", Trigger: contract.DeployManual}), contract.ErrConflict)
}
