package commands_test

import (
	"context"
	"testing"

	"zimam/agent/contract"
	"zimam/agent/internal/commands"
	"zimam/agent/internal/jobs"
	"zimam/agent/internal/sites"
)

func TestSiteRollbackWired(t *testing.T) {
	d, ok := contract.Lookup("site.rollback")
	if !ok || d.Kind != contract.KindJob || !d.Audited {
		t.Fatalf("site.rollback = %+v, %v; want an audited job", d, ok)
	}
	if got := d.Subject(&contract.SiteRollbackParams{Name: "shop"}); got != "site:shop" {
		t.Errorf("subject %q, want site:shop", got)
	}
	if l, ok := jobs.LimitsFor("site.rollback"); !ok || l != (jobs.Limits{MemoryMax: "512M", RuntimeMaxSec: 300}) {
		t.Errorf("limits %+v", l)
	}
	if commands.JobHandlers()["site.rollback"] == nil {
		t.Error("site.rollback has no job handler")
	}
	e := newPHPEnv(t)
	pre := commands.Prechecks(e.d)["site.rollback"]
	if pre == nil {
		t.Fatal("site.rollback has no precheck")
	}
	params := &contract.SiteRollbackParams{Name: "shop", Release: "20261115120000-abcdef0"}
	wantCode(t, pre(context.Background(), params), contract.ErrNotFound)
	if err := sites.Save(e.p, sites.State{Name: "shop", Domain: "shop.example.com", Type: contract.SiteStatic, TemplateVersion: 1, Security: sites.DefaultSecurity()}); err != nil {
		t.Fatal(err)
	}
	wantCode(t, pre(context.Background(), params), contract.ErrConflict)
}

// site.releases is a routine read: served without a job manager, never
// audited.
func TestSiteReleasesWired(t *testing.T) {
	d, ok := contract.Lookup("site.releases")
	if !ok || d.Kind != contract.KindRead || d.Audited {
		t.Fatalf("site.releases = %+v, %v; want an unaudited read", d, ok)
	}
	e := newPHPEnv(t)
	h := commands.New(e.d, nil)["site.releases"]
	if h == nil {
		t.Fatal("site.releases has no handler")
	}
	_, err := h(context.Background(), &contract.SiteNameParams{Name: "shop"})
	wantCode(t, err, contract.ErrNotFound)
}
