package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"zimam/agent/internal/audit"
	"zimam/agent/internal/config"
	"zimam/agent/internal/paths"
	"zimam/agent/internal/sysexec"
)

const setupLinkOutput = "https://panel.example.com/setup/abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQ\nPanel IP allowlist: empty\n"

// setupLinkDeps is a server under a temporary root where bootstrap has run,
// and a runner that answers zimam:setup-link with or without the flag.
func setupLinkDeps(t *testing.T) (sysexec.Deps, *sysexec.FakeRunner) {
	t.Helper()
	p := paths.Paths{Root: t.TempDir()}
	for _, dir := range []string{p.LogDir(), p.LocksDir()} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	argv := []string{"-u", "zimam", "--", "/usr/bin/php8.5", p.PanelCurrent() + "/artisan", "zimam:setup-link"}
	run := sysexec.NewFakeRunner(
		sysexec.Script{Path: "/usr/sbin/runuser", Args: argv, Result: sysexec.Result{Stdout: []byte(setupLinkOutput)}},
		sysexec.Script{Path: "/usr/sbin/runuser", Args: append(slices.Clone(argv), "--clear-ip-allowlist"), Result: sysexec.Result{Stdout: []byte(setupLinkOutput)}},
	)
	cfg := config.New("panel.example.com", "8.5", "8.5")
	return sysexec.Deps{Paths: p, Run: run, Config: cfg, Logf: t.Logf, Now: time.Now}, run
}

func runSetupLink(t *testing.T, d sysexec.Deps, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := setupLink(context.Background(), d, audit.Writer{Paths: d.Paths}, args, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

func readAudit(t *testing.T, p paths.Paths) []map[string]any {
	t.Helper()
	data, err := os.ReadFile(p.AuditLog())
	if err != nil {
		t.Fatal(err)
	}
	var entries []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		var e map[string]any
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatal(err)
		}
		entries = append(entries, e)
	}
	return entries
}

func TestPanelSetupLinkPrintsOutput(t *testing.T) {
	d, run := setupLinkDeps(t)
	code, stdout, stderr := runSetupLink(t, d)
	if code != 0 || stdout != setupLinkOutput {
		t.Fatalf("exit %d, stdout %q, stderr %q; want 0 and artisan's output as it is", code, stdout, stderr)
	}
	c := run.Calls()[0]
	if !slices.Contains(c.Env, "HOME=/home/zimam") || c.Dir != "/" {
		t.Fatalf("runuser ran with env %q in %q, want HOME=/home/zimam in /", c.Env, c.Dir)
	}
}

func TestSetupLinkCommandAuditedWithoutURL(t *testing.T) {
	d, _ := setupLinkDeps(t)
	if code, _, stderr := runSetupLink(t, d); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	entries := readAudit(t, d.Paths)
	if len(entries) != 1 {
		t.Fatalf("%d audit entries, want 1", len(entries))
	}
	e := entries[0]
	if e["actor"] != "cli:root" || e["command"] != "panel setup-link" || e["outcome"] != "ok" || e["peer_uid"] != nil {
		t.Fatalf("audit entry %v", e)
	}
	if raw := mustReadFile(t, d.Paths.AuditLog()); strings.Contains(raw, "/setup/") || strings.Contains(raw, "abcdefghijklmnop") {
		t.Fatalf("the audit log holds the link: %s", raw)
	}
}

func TestClearIpAllowlistPassedThrough(t *testing.T) {
	d, run := setupLinkDeps(t)
	if code, _, stderr := runSetupLink(t, d, "--clear-ip-allowlist"); code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if args := run.Calls()[0].Args; args[len(args)-1] != "--clear-ip-allowlist" {
		t.Fatalf("runuser %q, want --clear-ip-allowlist passed to artisan", args)
	}
	params := readAudit(t, d.Paths)[0]["params"].(map[string]any)
	if params["clear_ip_allowlist"] != true {
		t.Fatalf("audit params %v, want clear_ip_allowlist true", params)
	}
}

func TestPanelSetupLinkFailureAudited(t *testing.T) {
	d, _ := setupLinkDeps(t)
	d.Config.PanelPHP = "8.4" // nothing scripted for php8.4, so the run fails
	code, _, stderr := runSetupLink(t, d)
	if code != 1 || stderr == "" {
		t.Fatalf("exit %d, stderr %q; want 1 with the reason", code, stderr)
	}
	if e := readAudit(t, d.Paths)[0]; e["outcome"] != "error:internal" {
		t.Fatalf("audit entry %v, want error:internal", e)
	}
}

func TestPanelSetupLinkUsage(t *testing.T) {
	d, run := setupLinkDeps(t)
	if code, _, stderr := runSetupLink(t, d, "--other"); code != exitUsage || !strings.Contains(stderr, "usage:") {
		t.Fatalf("exit %d, stderr %q; want usage", code, stderr)
	}
	if len(run.Calls()) != 0 {
		t.Fatal("ran artisan with a bad flag")
	}
}

func mustReadFile(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
