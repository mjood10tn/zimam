package commands_test

import (
	"context"
	"testing"

	"zimam/agent/contract"
	"zimam/agent/internal/commands"
	"zimam/agent/internal/jobs"
)

func TestSitePHPWired(t *testing.T) {
	d, ok := contract.Lookup("site.php")
	if !ok || d.Kind != contract.KindJob || !d.Audited {
		t.Fatalf("site.php = %+v, %v; want an audited job", d, ok)
	}
	if got := d.Subject(&contract.SitePHPParams{Name: "shop"}); got != "site:shop" {
		t.Errorf("subject %q, want site:shop", got)
	}
	if l, ok := jobs.LimitsFor("site.php"); !ok || l != (jobs.Limits{MemoryMax: "512M", RuntimeMaxSec: 300}) {
		t.Errorf("limits %+v", l)
	}
	if commands.JobHandlers()["site.php"] == nil {
		t.Error("site.php has no job handler")
	}
	// The precheck is sites.CheckSwitch's: here, a site that doesn't exist.
	e := newPHPEnv(t)
	pre := commands.Prechecks(e.d)["site.php"]
	if pre == nil {
		t.Fatal("site.php has no precheck")
	}
	wantCode(t, pre(context.Background(), &contract.SitePHPParams{Name: "shop", PHPVersion: "8.4"}), contract.ErrNotFound)
}
