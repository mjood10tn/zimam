package commands_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"zimam/agent/contract"
	"zimam/agent/internal/commands"
	"zimam/agent/internal/config"
	"zimam/agent/internal/jobs"
	"zimam/agent/internal/sysexec"
)

func nodePrecheck(t *testing.T, d sysexec.Deps, command string, p contract.Params) error {
	t.Helper()
	pre := commands.Prechecks(d)[command]
	if pre == nil {
		t.Fatalf("%s has no precheck", command)
	}
	return pre(context.Background(), p)
}

// A major is installable through its last day of support, not after it.
func TestEndOfLifeMajorRefused(t *testing.T) {
	e := newPHPEnv(t)
	params := &contract.NodeInstallParams{Major: "24"}
	e.now = time.Date(2028, 4, 30, 23, 59, 0, 0, time.UTC)
	if err := nodePrecheck(t, e.d, "node.install", params); err != nil {
		t.Fatalf("on its last day, installing 24: %v, want allowed", err)
	}
	e.now = time.Date(2028, 5, 1, 0, 0, 0, 0, time.UTC)
	ae := wantCode(t, nodePrecheck(t, e.d, "node.install", params), contract.ErrValidationFailed)
	if !strings.Contains(ae.Fields["major"], "2028-04-30") {
		t.Errorf("fields = %v, want major to name the end date", ae.Fields)
	}
	// 26 is still supported on that day, and the default needs no major.
	if err := nodePrecheck(t, e.d, "node.install", &contract.NodeInstallParams{}); err != nil {
		t.Errorf("installing the default: %v", err)
	}
}

// writeJob puts a job's state on disk as jobs.Manager keeps it.
func writeJob(t *testing.T, e *phpEnv, id, subject, status string) {
	t.Helper()
	dir := e.p.JobDir(contract.JobID(id))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	st := `{"job":"` + id + `","command":"x","subject":"` + subject + `","status":"` + status + `","queued_at":"2026-11-03T09:00:00Z"}`
	if err := os.WriteFile(filepath.Join(dir, "state.json"), []byte(st), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestUninstallRefusedWhileSiteJobRuns(t *testing.T) {
	e := newPHPEnv(t)
	touch(t, e.p.NodeSources())
	e.dpkgNode("installed", "26.13.1-1nodesource1")
	writeJob(t, e, "01JBX7Q3A1B2C3D4E5F6G7H8J9", "site:shop", "running")
	// A finished site job and the uninstall's own job do not count.
	writeJob(t, e, "01JBX7Q3A1B2C3D4E5F6G7H8JA", "site:blog", "succeeded")
	writeJob(t, e, "01JBX7Q3A1B2C3D4E5F6G7H8JB", "server", "running")
	ae := wantCode(t, nodePrecheck(t, e.d, "node.uninstall", &contract.NodeUninstallParams{}), contract.ErrBusy)
	if !strings.Contains(ae.Message, "site") {
		t.Errorf("message = %q", ae.Message)
	}
	// The handler checks again: a site job may have started since.
	_, err := e.handleParams(t, "node.uninstall", &contract.NodeUninstallParams{})
	wantCode(t, err, contract.ErrBusy)

	writeJob(t, e, "01JBX7Q3A1B2C3D4E5F6G7H8J9", "site:shop", "succeeded")
	if err := nodePrecheck(t, e.d, "node.uninstall", &contract.NodeUninstallParams{}); err != nil {
		t.Errorf("with no site job running: %v", err)
	}
}

func TestUninstallRefusedWhenNotInstalled(t *testing.T) {
	e := newPHPEnv(t)
	e.run.Add(sysexec.Script{Path: "/usr/bin/dpkg-query", Args: []string{"-W", "-f=${db:Status-Status} ${Version}", "nodejs"}, Result: sysexec.Result{ExitCode: 1}})
	wantCode(t, nodePrecheck(t, e.d, "node.uninstall", &contract.NodeUninstallParams{}), contract.ErrNotFound)
}

// dpkgNode scripts dpkg-query's answer for the nodejs package.
func (e *phpEnv) fpmScripts() {
	for _, v := range []string{"8.3", "8.4", "8.5"} {
		e.run.Add(fpmActive(v, true))
	}
}

func (e *phpEnv) dpkgNode(status, version string) {
	e.run.Add(sysexec.Script{Path: "/usr/bin/dpkg-query", Args: []string{"-W", "-f=${db:Status-Status} ${Version}", "nodejs"}, Result: sysexec.Result{Stdout: []byte(status + " " + version)}})
}

// handleParams runs a handler with params as job run would.
func (e *phpEnv) handleParams(t *testing.T, command string, p contract.Params) (any, error) {
	t.Helper()
	cfg, err := config.Load(e.p)
	if err != nil {
		t.Fatal(err)
	}
	h := commands.JobHandlers()[command]
	if h == nil {
		t.Fatalf("%s has no job handler", command)
	}
	j := &jobs.Context{ID: "01JBX7Q3A1B2C3D4E5F6G7H8J9", Logf: t.Logf, Config: cfg, Paths: e.p, Run: e.run}
	return h(context.Background(), j, p)
}

func TestSystemStatusReportsNode(t *testing.T) {
	e := newPHPEnv(t)
	e.fpmScripts()
	e.dpkgNode("installed", "26.13.1-1nodesource1")
	n := getStatus(t, e.d).Node
	if n == nil || !n.Installed || n.Major != "26" || n.Version != "26.13.1-1nodesource1" {
		t.Fatalf("node = %+v", n)
	}
	if len(n.Available) != 2 || n.Available[0].Major != "24" || n.Available[0].EndOfLife != "2028-04-30" || !n.Available[0].Supported || n.Available[0].Recommended || !n.Available[1].Recommended {
		t.Errorf("available = %+v", n.Available)
	}

	e = newPHPEnv(t)
	e.fpmScripts()
	e.run.Add(sysexec.Script{Path: "/usr/bin/dpkg-query", Args: []string{"-W", "-f=${db:Status-Status} ${Version}", "nodejs"}, Result: sysexec.Result{ExitCode: 1}})
	if n := getStatus(t, e.d).Node; n == nil || n.Installed || n.Version != "" || len(n.Available) != 2 {
		t.Errorf("not installed: node = %+v", n)
	}

	// dpkg-query that fails leaves Node unknown, not the status failed.
	e = newPHPEnv(t)
	e.fpmScripts()
	if n := getStatus(t, e.d).Node; n != nil {
		t.Errorf("unscripted dpkg-query: node = %+v, want nil", n)
	}
}
