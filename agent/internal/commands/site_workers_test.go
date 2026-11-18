package commands_test

import (
	"context"
	"testing"

	"zimam/agent/contract"
	"zimam/agent/internal/commands"
	"zimam/agent/internal/sites"
)

func TestSiteWorkersWired(t *testing.T) {
	for _, name := range []string{"site.workers", "site.workers.restart"} {
		if commands.JobHandlers()[name] == nil {
			t.Errorf("%s has no job handler", name)
		}
	}
	e := newPHPEnv(t)
	pre := commands.Prechecks(e.d)
	ctx := context.Background()
	wantCode(t, pre["site.workers"](ctx, &contract.SiteWorkersParams{Name: "shop", Workers: contract.WorkerList{}}), contract.ErrNotFound)

	// A static site has no PHP to run a worker.
	static := sites.State{Name: "blog", Domain: "blog.example.com", Type: contract.SiteStatic, TemplateVersion: 3, Security: sites.DefaultSecurity()}
	if err := sites.Save(e.p, static); err != nil {
		t.Fatal(err)
	}
	wantCode(t, pre["site.workers"](ctx, &contract.SiteWorkersParams{Name: "blog", Workers: contract.WorkerList{}}), contract.ErrConflict)

	shop := sites.State{Name: "shop", Domain: "shop.example.com", Type: contract.SiteLaravel, PHPVersion: "8.4", TemplateVersion: 3, Security: sites.DefaultSecurity(),
		Workers: []sites.WorkerState{{ID: "q", Kind: contract.WorkerLaravelQueue, Processes: 1}}}
	if err := sites.Save(e.p, shop); err != nil {
		t.Fatal(err)
	}
	if err := pre["site.workers"](ctx, &contract.SiteWorkersParams{Name: "shop", Workers: contract.WorkerList{}}); err != nil {
		t.Errorf("a laravel site: %v", err)
	}
	// A restart names a worker the state lists, or none.
	if err := pre["site.workers.restart"](ctx, &contract.SiteWorkersRestartParams{Name: "shop", Worker: "q"}); err != nil {
		t.Errorf("restart q: %v", err)
	}
	wantCode(t, pre["site.workers.restart"](ctx, &contract.SiteWorkersRestartParams{Name: "shop", Worker: "nope"}), contract.ErrNotFound)

	// The status read answers not_found for a missing site, and an empty
	// list for a site without workers, with no systemctl call.
	status := commands.New(e.d, nil)["site.workers.status"]
	_, err := status(ctx, &contract.SiteWorkersStatusParams{Name: "gone"})
	wantCode(t, err, contract.ErrNotFound)
	res, err := status(ctx, &contract.SiteWorkersStatusParams{Name: "blog"})
	if r, ok := res.(contract.SiteWorkersStatusResult); err != nil || !ok || len(r.Workers) != 0 || r.Workers == nil {
		t.Errorf("blog: %+v, %v", res, err)
	}
}

func TestSiteCronWired(t *testing.T) {
	if commands.JobHandlers()["site.cron"] == nil {
		t.Fatal("site.cron has no job handler")
	}
	e := newPHPEnv(t)
	pre := commands.Prechecks(e.d)["site.cron"]
	ctx := context.Background()
	wantCode(t, pre(ctx, &contract.SiteCronParams{Name: "shop", Jobs: contract.CronJobList{}}), contract.ErrNotFound)

	static := sites.State{Name: "blog", Domain: "blog.example.com", Type: contract.SiteStatic, TemplateVersion: 3, Security: sites.DefaultSecurity()}
	if err := sites.Save(e.p, static); err != nil {
		t.Fatal(err)
	}
	// A static site has no Laravel to schedule, but a plain job runs.
	wantCode(t, pre(ctx, &contract.SiteCronParams{Name: "blog", Scheduler: true, Jobs: contract.CronJobList{}}), contract.ErrConflict)
	if err := pre(ctx, &contract.SiteCronParams{Name: "blog", Jobs: contract.CronJobList{{ID: "a", Schedule: "0 * * * *", Command: "date", Enabled: true}}}); err != nil {
		t.Errorf("a job on a static site: %v", err)
	}

	shop := sites.State{Name: "shop", Domain: "shop.example.com", Type: contract.SiteLaravel, PHPVersion: "8.4", TemplateVersion: 3, Security: sites.DefaultSecurity()}
	if err := sites.Save(e.p, shop); err != nil {
		t.Fatal(err)
	}
	if err := pre(ctx, &contract.SiteCronParams{Name: "shop", Scheduler: true, Jobs: contract.CronJobList{}}); err != nil {
		t.Errorf("a laravel site: %v", err)
	}
}
