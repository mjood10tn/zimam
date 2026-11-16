package commands_test

import (
	"context"
	"testing"

	"zimam/agent/contract"
	"zimam/agent/internal/commands"
	"zimam/agent/internal/jobs"
	"zimam/agent/internal/sites"
)

// site.env.read is a read served without a job manager, audited by the
// dispatcher because it returns a secret (R02-D10).
func TestSiteEnvReadWired(t *testing.T) {
	d, ok := contract.Lookup("site.env.read")
	if !ok || d.Kind != contract.KindRead || !d.Audited {
		t.Fatalf("site.env.read = %+v, %v; want an audited read", d, ok)
	}
	e := newPHPEnv(t)
	h := commands.New(e.d, nil)["site.env.read"]
	if h == nil {
		t.Fatal("site.env.read has no handler")
	}
	_, err := h(context.Background(), &contract.SiteNameParams{Name: "shop"})
	wantCode(t, err, contract.ErrNotFound)
}

func TestSiteEnvWriteWired(t *testing.T) {
	d, ok := contract.Lookup("site.env.write")
	if !ok || d.Kind != contract.KindJob || !d.Audited {
		t.Fatalf("site.env.write = %+v, %v; want an audited job", d, ok)
	}
	params := &contract.SiteEnvWriteParams{Name: "shop", Content: "APP_KEY=dummy\n"}
	if got := d.Subject(params); got != "site:shop" {
		t.Errorf("subject %q, want site:shop", got)
	}
	if l, ok := jobs.LimitsFor("site.env.write"); !ok || l != (jobs.Limits{MemoryMax: "256M", RuntimeMaxSec: 120}) {
		t.Errorf("limits %+v", l)
	}
	if commands.JobHandlers()["site.env.write"] == nil {
		t.Error("site.env.write has no job handler")
	}
	e := newPHPEnv(t)
	pre := commands.Prechecks(e.d)["site.env.write"]
	if pre == nil {
		t.Fatal("site.env.write has no precheck")
	}
	wantCode(t, pre(context.Background(), params), contract.ErrNotFound)
	if err := sites.Save(e.p, sites.State{Name: "shop", Domain: "shop.example.com", Type: contract.SiteStatic, TemplateVersion: 1, Security: sites.DefaultSecurity()}); err != nil {
		t.Fatal(err)
	}
	if err := pre(context.Background(), params); err != nil {
		t.Errorf("precheck of an existing site = %v, want nil", err)
	}
}
