package backup

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"slices"
	"strings"
	"testing"

	"zimam/agent/contract"
	"zimam/agent/internal/sites"
	"zimam/agent/internal/sysexec"
)

const (
	systemctlPath = "/usr/bin/systemctl"
	analyzePath   = "/usr/bin/systemd-analyze"
)

// addSite saves a site's state with its databases.
func addSite(t *testing.T, e env, name contract.SiteName, dbs ...contract.DatabaseName) {
	t.Helper()
	st := sites.State{Name: name, Domain: contract.Domain(name + ".example.com"), Type: contract.SiteStatic, TemplateVersion: 1,
		Security: sites.DefaultSecurity(), Databases: dbs}
	if err := sites.Save(e.d.Paths, st); err != nil {
		t.Fatal(err)
	}
}

// planEnv has the offsite destination, site shop with s_shop_main, site
// blog, and the systemd calls a plan makes scripted.
func planEnv(t *testing.T, scripts ...sysexec.Script) env {
	t.Helper()
	e := newEnv(t, scripts...)
	os.MkdirAll(e.d.Paths.SystemdUnitDir(), 0o755)
	if _, err := SetDestination(context.Background(), e.d, s3Params()); err != nil {
		t.Fatal(err)
	}
	addSite(t, e, "shop", "s_shop_main")
	addSite(t, e, "blog")
	for _, plan := range []string{"nightly", "weekly"} {
		svc, timer := e.d.Paths.SystemdUnit("zimam-backup-"+plan+".service"), e.d.Paths.SystemdUnit("zimam-backup-"+plan+".timer")
		for _, s := range []sysexec.Script{
			{Path: analyzePath, Args: []string{"verify", svc, timer}},
			{Path: systemctlPath, Args: []string{"enable", "--now", "zimam-backup-" + plan + ".timer"}},
			{Path: systemctlPath, Args: []string{"restart", "zimam-backup-" + plan + ".timer"}},
			{Path: systemctlPath, Args: []string{"disable", "--now", "zimam-backup-" + plan + ".timer"}},
		} {
			e.fake.Add(s)
		}
	}
	e.fake.Add(sysexec.Script{Path: systemctlPath, Args: []string{"daemon-reload"}})
	return e
}

func nightlyPlan() *contract.BackupPlanParams {
	return &contract.BackupPlanParams{ID: "nightly", Destination: "offsite", Sites: []contract.SiteName{"shop"},
		Databases: []contract.DatabaseName{"s_shop_main"}, Schedule: "daily 03:30", Retention: contract.Retention{KeepLast: 7}}
}

func systemctlCalls(f *sysexec.FakeRunner) []string {
	var out []string
	for _, c := range f.Calls() {
		if c.Path == systemctlPath {
			out = append(out, strings.Join(c.Args, " "))
		}
	}
	return out
}

func TestPlanRendersTimer(t *testing.T) {
	e := planEnv(t)
	if _, err := SetPlan(context.Background(), e.d, nightlyPlan()); err != nil {
		t.Fatal(err)
	}
	timer, err := os.ReadFile(e.d.Paths.SystemdUnit("zimam-backup-nightly.timer"))
	if err != nil || !strings.Contains(string(timer), "OnCalendar=*-*-* 03:30:00\n") {
		t.Fatalf("timer %q, %v", timer, err)
	}
	if svc, err := os.ReadFile(e.d.Paths.SystemdUnit("zimam-backup-nightly.service")); err != nil || !strings.Contains(string(svc), "ExecStart=/usr/local/sbin/zimam-agent backup run nightly\n") {
		t.Errorf("service %q, %v", svc, err)
	}
	plans, err := loadPlans(e.d.Paths)
	if err != nil || len(plans) != 1 || plans[0].ID != "nightly" {
		t.Fatalf("plans %+v, %v", plans, err)
	}
	if got := systemctlCalls(e.fake); !slices.Equal(got, []string{"daemon-reload", "enable --now zimam-backup-nightly.timer"}) {
		t.Errorf("systemctl %q", got)
	}

	// A new schedule replaces the plan and restarts its timer on it.
	p := nightlyPlan()
	p.Schedule = "weekly mon 01:00"
	if _, err := SetPlan(context.Background(), e.d, p); err != nil {
		t.Fatal(err)
	}
	timer, _ = os.ReadFile(e.d.Paths.SystemdUnit("zimam-backup-nightly.timer"))
	if !strings.Contains(string(timer), "OnCalendar=Mon *-*-* 01:00:00\n") {
		t.Errorf("timer after the edit %q", timer)
	}
	if got := systemctlCalls(e.fake); got[len(got)-1] != "restart zimam-backup-nightly.timer" {
		t.Errorf("systemctl %q, want a restart last", got)
	}
	if plans, _ := loadPlans(e.d.Paths); len(plans) != 1 || plans[0].Schedule != "weekly mon 01:00" {
		t.Errorf("plans %+v", plans)
	}
}

// A plan names only what exists: its destination, sites and databases.
func TestPlanRefusesWhatDoesNotExist(t *testing.T) {
	e := planEnv(t)
	for _, change := range []func(*contract.BackupPlanParams){
		func(p *contract.BackupPlanParams) { p.Destination = "elsewhere" },
		func(p *contract.BackupPlanParams) { p.Sites = []contract.SiteName{"gone"} },
		func(p *contract.BackupPlanParams) { p.Databases = []contract.DatabaseName{"s_shop_other"} },
	} {
		p := nightlyPlan()
		change(p)
		_, err := SetPlan(context.Background(), e.d, p)
		var ae *contract.AgentError
		if !errors.As(err, &ae) || ae.Code != contract.ErrNotFound {
			t.Errorf("%+v: err = %v, want not_found", p, err)
		}
	}
	if _, err := os.Stat(e.d.Paths.BackupPlansState()); !errors.Is(err, fs.ErrNotExist) {
		t.Error("a refused plan was saved")
	}
}

func TestPlanDeleteRemovesUnits(t *testing.T) {
	e := planEnv(t)
	if _, err := SetPlan(context.Background(), e.d, nightlyPlan()); err != nil {
		t.Fatal(err)
	}
	weekly := nightlyPlan()
	weekly.ID, weekly.Schedule = "weekly", "weekly sun 02:00"
	if _, err := SetPlan(context.Background(), e.d, weekly); err != nil {
		t.Fatal(err)
	}
	stamp := e.d.Paths.TimerStamp("zimam-backup-nightly.timer")
	os.MkdirAll(stamp[:strings.LastIndex(stamp, "/")], 0o755)
	os.WriteFile(stamp, nil, 0o644)
	if _, err := DeletePlan(context.Background(), e.d, "nightly"); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{stamp, e.d.Paths.SystemdUnit("zimam-backup-nightly.timer"), e.d.Paths.SystemdUnit("zimam-backup-nightly.service")} {
		if _, err := os.Lstat(f); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("%s is still there", f)
		}
	}
	if _, err := os.Lstat(e.d.Paths.SystemdUnit("zimam-backup-weekly.timer")); err != nil {
		t.Errorf("the other plan's timer went: %v", err)
	}
	got := systemctlCalls(e.fake)
	if !slices.Contains(got, "disable --now zimam-backup-nightly.timer") || got[len(got)-1] != "daemon-reload" {
		t.Errorf("systemctl %q", got)
	}
	if plans, _ := loadPlans(e.d.Paths); len(plans) != 1 || plans[0].ID != "weekly" {
		t.Errorf("plans %+v", plans)
	}
	_, err := DeletePlan(context.Background(), e.d, "nightly")
	var ae *contract.AgentError
	if !errors.As(err, &ae) || ae.Code != contract.ErrNotFound {
		t.Errorf("deleting it again: %v, want not_found", err)
	}
}

// A destroyed site, or a dropped database, leaves every plan; the plans
// and their backups stay (R05-D13).
func TestDestroyRemovesSiteFromPlans(t *testing.T) {
	e := planEnv(t)
	p := nightlyPlan()
	p.Sites = []contract.SiteName{"shop", "blog"}
	if _, err := SetPlan(context.Background(), e.d, p); err != nil {
		t.Fatal(err)
	}
	if err := ForgetSite(e.d.Paths, "shop"); err != nil {
		t.Fatal(err)
	}
	if err := ForgetDatabase(e.d.Paths, "s_shop_main"); err != nil {
		t.Fatal(err)
	}
	plans, _ := loadPlans(e.d.Paths)
	if len(plans) != 1 || !slices.Equal(plans[0].Sites, []contract.SiteName{"blog"}) || len(plans[0].Databases) != 0 {
		t.Errorf("plans %+v", plans)
	}
	if err := ForgetSite(e.d.Paths, "nothing"); err != nil {
		t.Errorf("a site in no plan: %v", err)
	}
}
