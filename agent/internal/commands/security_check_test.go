package commands_test

import (
	"context"
	"testing"

	"zimam/agent/contract"
	"zimam/agent/internal/commands"
	"zimam/agent/internal/jobs"
)

// security.check is an audited job (§5.3): the job system writes its
// accepted entry before the job exists and its outcome when it ends.
func TestSecurityCheckAudited(t *testing.T) {
	d, ok := contract.Lookup("security.check")
	if !ok || d.Kind != contract.KindJob || !d.Audited {
		t.Fatalf("security.check = %+v, %v; want an audited job", d, ok)
	}
	for p, want := range map[*contract.SecurityCheckParams]string{
		{Scope: contract.CheckScopeServer}:             "server",
		{Scope: contract.CheckScopeSite, Name: "shop"}: "site:shop",
	} {
		if got := d.Subject(p); got != want {
			t.Errorf("subject %q, want %q", got, want)
		}
	}
	if l, ok := jobs.LimitsFor("security.check"); !ok || l != (jobs.Limits{MemoryMax: "512M", RuntimeMaxSec: 600}) {
		t.Errorf("limits %+v", l)
	}
	if commands.JobHandlers()["security.check"] == nil {
		t.Error("security.check has no job handler")
	}
	e := newPHPEnv(t)
	pre := commands.Prechecks(e.d)["security.check"]
	wantCode(t, pre(context.Background(), &contract.SecurityCheckParams{Scope: contract.CheckScopeSite, Name: "shop"}), contract.ErrNotFound)
	if err := pre(context.Background(), &contract.SecurityCheckParams{Scope: contract.CheckScopeServer}); err != nil {
		t.Errorf("server scope: %v", err)
	}
}
