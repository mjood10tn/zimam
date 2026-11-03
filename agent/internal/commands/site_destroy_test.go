package commands_test

import (
	"context"
	"testing"

	"zimam/agent/contract"
	"zimam/agent/internal/commands"
	"zimam/agent/internal/jobs"
)

func TestSiteDestroyWired(t *testing.T) {
	d, ok := contract.Lookup("site.destroy")
	if !ok || d.Kind != contract.KindJob || !d.Audited {
		t.Fatalf("site.destroy = %+v, %v; want an audited job", d, ok)
	}
	if got := d.Subject(&contract.SiteNameParams{Name: "shop"}); got != "site:shop" {
		t.Errorf("subject %q, want site:shop", got)
	}
	if l, ok := jobs.LimitsFor("site.destroy"); !ok || l != (jobs.Limits{MemoryMax: "512M", RuntimeMaxSec: 300}) {
		t.Errorf("limits %+v", l)
	}
	if commands.JobHandlers()["site.destroy"] == nil {
		t.Error("site.destroy has no job handler")
	}
	// The precheck is sites.CheckDestroy's: here, a site with nothing on
	// the server.
	e := newPHPEnv(t)
	pre := commands.Prechecks(e.d)["site.destroy"]
	if pre == nil {
		t.Fatal("site.destroy has no precheck")
	}
	wantCode(t, pre(context.Background(), &contract.SiteNameParams{Name: "shop"}), contract.ErrNotFound)
}
