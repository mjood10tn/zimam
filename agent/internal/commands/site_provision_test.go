package commands_test

import (
	"context"
	"testing"

	"zimam/agent/contract"
	"zimam/agent/internal/commands"
	"zimam/agent/internal/jobs"
)

func TestSiteProvisionWired(t *testing.T) {
	d, ok := contract.Lookup("site.provision")
	if !ok || d.Kind != contract.KindJob || !d.Audited {
		t.Fatalf("site.provision = %+v, %v; want an audited job", d, ok)
	}
	if got := d.Subject(&contract.SiteProvisionParams{Name: "shop"}); got != "site:shop" {
		t.Errorf("subject %q, want site:shop", got)
	}
	if l, ok := jobs.LimitsFor("site.provision"); !ok || l != (jobs.Limits{MemoryMax: "512M", RuntimeMaxSec: 300}) {
		t.Errorf("limits %+v", l)
	}
	if commands.JobHandlers()["site.provision"] == nil {
		t.Error("site.provision has no job handler")
	}
	// The precheck is sites.Check's, against serve's view: here, the panel's
	// own domain.
	e := newPHPEnv(t)
	pre := commands.Prechecks(e.d)["site.provision"]
	if pre == nil {
		t.Fatal("site.provision has no precheck")
	}
	err := pre(context.Background(), &contract.SiteProvisionParams{Name: "shop", Domain: "panel.example.com", Type: contract.SiteStatic})
	wantCode(t, err, contract.ErrConflict)
	if err := pre(context.Background(), &contract.SiteProvisionParams{Name: "shop", Domain: "shop.example.com", Type: contract.SiteStatic}); err != nil {
		t.Errorf("a new static site: %v", err)
	}
}
