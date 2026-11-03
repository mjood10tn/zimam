package commands_test

import (
	"context"
	"testing"

	"zimam/agent/contract"
	"zimam/agent/internal/commands"
	"zimam/agent/internal/jobs"
)

func TestSiteSecurityWired(t *testing.T) {
	d, ok := contract.Lookup("site.security")
	if !ok || d.Kind != contract.KindJob || !d.Audited {
		t.Fatalf("site.security = %+v, %v; want an audited job", d, ok)
	}
	if got := d.Subject(&contract.SiteSecurityParams{Name: "shop"}); got != "site:shop" {
		t.Errorf("subject %q, want site:shop", got)
	}
	if l, ok := jobs.LimitsFor("site.security"); !ok || l != (jobs.Limits{MemoryMax: "512M", RuntimeMaxSec: 300}) {
		t.Errorf("limits %+v", l)
	}
	if commands.JobHandlers()["site.security"] == nil {
		t.Error("site.security has no job handler")
	}
	// The precheck is sites.CheckSecurity's: here, a site that doesn't exist.
	e := newPHPEnv(t)
	pre := commands.Prechecks(e.d)["site.security"]
	if pre == nil {
		t.Fatal("site.security has no precheck")
	}
	wantCode(t, pre(context.Background(), &contract.SiteSecurityParams{Name: "shop"}), contract.ErrNotFound)
}
