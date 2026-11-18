package commands

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"strings"
	"testing"
	"time"

	"zimam/agent/contract"
	"zimam/agent/internal/helper"
	"zimam/agent/internal/jobs"
	"zimam/agent/internal/paths"
	"zimam/agent/internal/sites"
	"zimam/agent/internal/sysexec"
)

// opsHelper records every helper operation, as "<op> <path>".
type opsHelper struct{ ops []string }

func (h *opsHelper) Exec(_ context.Context, _ helper.Account, r helper.ExecRequest) (int, error) {
	h.ops = append(h.ops, "exec "+r.Program+" "+string(r.PHP))
	return 0, nil
}
func (h *opsHelper) Mkdir(_ context.Context, _ helper.Account, name string, _ fs.FileMode) error {
	h.ops = append(h.ops, "mkdir "+name)
	return nil
}
func (h *opsHelper) Write(_ context.Context, _ helper.Account, name string, _ fs.FileMode, r io.Reader) error {
	b, _ := io.ReadAll(r)
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	h.ops = append(h.ops, "write "+name+" "+lines[len(lines)-1]) // the last line, past the header
	return nil
}
func (h *opsHelper) List(_ context.Context, _ helper.Account, dir string) (helper.Listing, error) {
	h.ops = append(h.ops, "list "+dir)
	return helper.Listing{}, nil
}
func (h *opsHelper) RemoveTree(_ context.Context, _ helper.Account, name string) error {
	h.ops = append(h.ops, "remove-tree "+name)
	return nil
}
func (h *opsHelper) Read(_ context.Context, _ helper.Account, name string, _, _ int64) (helper.ReadResult, error) {
	h.ops = append(h.ops, "read "+name)
	return helper.ReadResult{}, fs.ErrNotExist
}

const systemctlBin = "/usr/bin/systemctl"

// workerDeps is a site shop on PHP 8.5, with a two-process worker when
// withWorkers, and a runner that answers a restart's show and restart.
func workerDeps(t *testing.T, withWorkers bool) (sysexec.Deps, *sysexec.FakeRunner) {
	t.Helper()
	units := []string{"zimam-worker-shop_q@1.service", "zimam-worker-shop_q@2.service"}
	run := sysexec.NewFakeRunner(
		sysexec.Script{Path: systemctlBin, Args: append([]string{"show", "--timestamp=unix", "-p", "Id,ActiveState", "--"}, units...),
			Result: sysexec.Result{Stdout: []byte("Id=" + units[0] + "\nActiveState=active\n\nId=" + units[1] + "\nActiveState=active\n")}},
		sysexec.Script{Path: systemctlBin, Args: append([]string{"restart", "--no-block"}, units...)},
	)
	d := sysexec.Deps{Paths: paths.Paths{Root: t.TempDir()}, Run: run, Logf: t.Logf, Now: time.Now}
	st := sites.State{Name: "shop", Domain: "shop.example.com", Type: contract.SiteLaravel, PHPVersion: "8.5", TemplateVersion: 3, Security: sites.DefaultSecurity()}
	if withWorkers {
		st.Workers = []sites.WorkerState{{ID: "q", Kind: contract.WorkerLaravelQueue, Processes: 2}}
	}
	if err := sites.Save(d.Paths, st); err != nil {
		t.Fatal(err)
	}
	return d, run
}

func restarts(run *sysexec.FakeRunner) []string {
	var out []string
	for _, c := range run.Calls() {
		if c.Args[0] == "restart" {
			out = append(out, strings.Join(c.Args, " "))
		}
	}
	return out
}

func TestEnvWriteRestartsWorkers(t *testing.T) {
	d, run := workerDeps(t, true)
	if _, err := afterEnvWrite(context.Background(), d, contract.SiteNameResult{Name: "shop"}); err != nil {
		t.Fatal(err)
	}
	if got := restarts(run); len(got) != 1 || got[0] != "restart --no-block zimam-worker-shop_q@1.service zimam-worker-shop_q@2.service" {
		t.Errorf("restarts = %q", got)
	}
}

func TestEnvWriteWithoutWorkersRunsNoSystemctl(t *testing.T) {
	d, run := workerDeps(t, false)
	if _, err := afterEnvWrite(context.Background(), d, contract.SiteNameResult{Name: "shop"}); err != nil {
		t.Fatal(err)
	}
	if calls := run.Calls(); len(calls) != 0 {
		t.Errorf("calls = %+v, want none", calls)
	}
}

// A failed restart keeps the saved .env: the job fails as landed.
func TestEnvWriteRestartFailureIsLanded(t *testing.T) {
	d, _ := workerDeps(t, true)
	d.Run = sysexec.NewFakeRunner() // every call fails
	_, err := afterEnvWrite(context.Background(), d, contract.SiteNameResult{Name: "shop"})
	var landed *jobs.Landed
	if !errors.As(err, &landed) || !strings.Contains(err.Error(), ".env is saved") {
		t.Errorf("err = %v, want a landed failure", err)
	}
}

func TestPHPSwitchRewritesSiteEnvOnlyAndRestarts(t *testing.T) {
	d, run := workerDeps(t, true)
	h := &opsHelper{}
	lookup := func(string) (helper.Account, error) { return helper.Account{Name: "s_shop", Home: "/home/s_shop"}, nil }
	if err := afterPHPSwitch(context.Background(), d, h, lookup, "shop"); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"mkdir .zimam",
		`write .zimam/site.env.sh export PATH="$HOME/.zimam/php8.5:/usr/local/bin:/usr/bin:/bin"`,
		"exec php 8.5",
	}
	if strings.Join(h.ops, "\n") != strings.Join(want, "\n") {
		t.Errorf("helper ops:\n%s\nwant:\n%s", strings.Join(h.ops, "\n"), strings.Join(want, "\n"))
	}
	for _, op := range h.ops {
		if strings.Contains(op, ".zimam/workers") || strings.Contains(op, ".zimam/cron") {
			t.Errorf("a script was touched: %s", op)
		}
	}
	if got := restarts(run); len(got) != 1 {
		t.Errorf("restarts = %q, want one", got)
	}
}
