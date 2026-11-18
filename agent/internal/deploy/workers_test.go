package deploy

import (
	"strings"
	"testing"

	"zimam/agent/contract"
	"zimam/agent/internal/sites"
	"zimam/agent/internal/sysexec"
)

const systemctlPath = "/usr/bin/systemctl"

var workerUnits = []string{"zimam-worker-shop_q@1.service", "zimam-worker-shop_q@2.service"}

// withWorkers gives the site a worker with two processes, and answers the
// systemctl calls a restart makes: show (both running), then the restart,
// which fails when restartFails is set.
func (e *deployEnv) withWorkers(restartFails bool) *sysexec.FakeRunner {
	e.t.Helper()
	st := e.state()
	st.Workers = []sites.WorkerState{{ID: "q", Kind: contract.WorkerLaravelQueue, Processes: 2}}
	wantNoErr(e.t, sites.Save(e.p, st))
	show := "Id=zimam-worker-shop_q@1.service\nActiveState=active\n\nId=zimam-worker-shop_q@2.service\nActiveState=active\n"
	restart := sysexec.Result{}
	if restartFails {
		restart.ExitCode = 1
	}
	run := sysexec.NewFakeRunner(
		sysexec.Script{Path: systemctlPath, Args: append([]string{"show", "--timestamp=unix", "-p", "Id,ActiveState", "--"}, workerUnits...), Result: sysexec.Result{Stdout: []byte(show)}},
		sysexec.Script{Path: systemctlPath, Args: append([]string{"restart", "--no-block"}, workerUnits...), Result: restart},
	)
	e.d.Run = run
	return run
}

func restarted(run *sysexec.FakeRunner) int {
	n := 0
	for _, c := range run.Calls() {
		if c.Path == systemctlPath && len(c.Args) > 0 && c.Args[0] == "restart" {
			n++
		}
	}
	return n
}

func TestDeployRestartsWorkers(t *testing.T) {
	e := newDeployEnv(t, contract.SiteLaravel)
	run := e.withWorkers(false)
	e.up.commit(map[string]string{"public/index.php": "<?php"})
	e.mustDeploy(contract.SiteDeployParams{})
	if restarted(run) != 1 {
		t.Errorf("calls %+v; want one restart of both processes by name", run.Calls())
	}
}

// A restart that fails after the swap leaves the release live and says so.
func TestDeployRestartFailureKeepsRelease(t *testing.T) {
	e := newDeployEnv(t, contract.SiteLaravel)
	e.withWorkers(true)
	e.up.commit(map[string]string{"public/index.php": "<?php"})
	res, err := e.deploy(contract.SiteDeployParams{})
	if err == nil || res.Release == "" || !strings.Contains(err.Error(), "is live, but restarting the site's workers failed") {
		t.Fatalf("deploy = %+v, %v; want the release live and the restart's failure", res, err)
	}
	if e.current() != "releases/"+string(res.Release) {
		t.Errorf("current -> %s", e.current())
	}
}

func TestRollbackRestartsWorkers(t *testing.T) {
	e := newDeployEnv(t, contract.SiteLaravel)
	rel := e.deployN(2)
	run := e.withWorkers(false)
	if _, err := e.rollback(rel[0]); err != nil {
		t.Fatal(err)
	}
	if restarted(run) != 1 {
		t.Errorf("calls %+v; want one restart", run.Calls())
	}
}
