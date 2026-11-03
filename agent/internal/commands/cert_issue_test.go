package commands_test

import (
	"context"
	"testing"

	"zimam/agent/contract"
	"zimam/agent/internal/commands"
	"zimam/agent/internal/jobs"
)

func TestCertIssueWired(t *testing.T) {
	d, ok := contract.Lookup("cert.issue")
	if !ok || d.Kind != contract.KindJob || !d.Audited {
		t.Fatalf("cert.issue = %+v, %v; want an audited job", d, ok)
	}
	if got := d.Subject(&contract.SiteNameParams{Name: "shop"}); got != "site:shop" {
		t.Errorf("subject %q, want site:shop", got)
	}
	if l, ok := jobs.LimitsFor("cert.issue"); !ok || l != (jobs.Limits{MemoryMax: "512M", RuntimeMaxSec: 300}) {
		t.Errorf("limits %+v", l)
	}
	if commands.JobHandlers()["cert.issue"] == nil {
		t.Error("cert.issue has no job handler")
	}
	// The precheck is sites.CheckCertificate's: here, a site that doesn't
	// exist.
	e := newPHPEnv(t)
	pre := commands.Prechecks(e.d)["cert.issue"]
	if pre == nil {
		t.Fatal("cert.issue has no precheck")
	}
	wantCode(t, pre(context.Background(), &contract.SiteNameParams{Name: "shop"}), contract.ErrNotFound)
}
