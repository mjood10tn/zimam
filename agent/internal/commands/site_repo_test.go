package commands_test

import (
	"context"
	"testing"

	"zimam/agent/contract"
	"zimam/agent/internal/commands"
	"zimam/agent/internal/jobs"
)

func TestSiteRepoWired(t *testing.T) {
	d, ok := contract.Lookup("site.repo")
	if !ok || d.Kind != contract.KindJob || !d.Audited {
		t.Fatalf("site.repo = %+v, %v; want an audited job", d, ok)
	}
	if got := d.Subject(&contract.SiteRepoParams{Name: "shop"}); got != "site:shop" {
		t.Errorf("subject %q, want site:shop", got)
	}
	if l, ok := jobs.LimitsFor("site.repo"); !ok || l != (jobs.Limits{MemoryMax: "512M", RuntimeMaxSec: 600}) {
		t.Errorf("limits %+v", l)
	}
	if commands.JobHandlers()["site.repo"] == nil {
		t.Error("site.repo has no job handler")
	}
	// The precheck is deploy.CheckRepo's: here, a site that doesn't exist.
	e := newPHPEnv(t)
	pre := commands.Prechecks(e.d)["site.repo"]
	if pre == nil {
		t.Fatal("site.repo has no precheck")
	}
	wantCode(t, pre(context.Background(), &contract.SiteRepoParams{Name: "shop", Repository: "octo/app", Branch: "main"}), contract.ErrNotFound)
}
