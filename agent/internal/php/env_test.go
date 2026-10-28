package php

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"zimam/agent/contract"
	"zimam/agent/internal/config"
	"zimam/agent/internal/paths"
	"zimam/agent/internal/sysexec"
)

// testEnv is a server's file tree under a temporary root, a FakeRunner, and
// an HTTPS server that serves a keyring as <php_repo_url>apt.gpg.
type testEnv struct {
	d   sysexec.Deps
	run *sysexec.FakeRunner
	p   paths.Paths
}

func newEnv(t *testing.T, keyring []byte, repoPath string) *testEnv {
	t.Helper()
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != repoPath+"apt.gpg" {
			http.NotFound(w, r)
			return
		}
		w.Write(keyring)
	}))
	t.Cleanup(srv.Close)
	p := paths.Paths{Root: t.TempDir()}
	// What installing php8.4-fpm and starting the agent would have created.
	mkdir(t, filepath.Dir(p.PHPIni("8.4")))
	mkdir(t, p.PHPPoolDir("8.4"))
	mkdir(t, p.LocksDir())
	cfg := config.New("panel.example.com", "8.4", "8.4")
	cfg.PHPRepoURL = srv.URL + repoPath
	run := sysexec.NewFakeRunner()
	return &testEnv{
		d:   sysexec.Deps{Paths: p, Run: run, Config: cfg, HTTP: srv.Client(), Logf: t.Logf, Now: time.Now},
		run: run,
		p:   p,
	}
}

func mkdir(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

const lockTimeout = "DPkg::Lock::Timeout=300"

var aptUpdate = sysexec.Script{Path: "/usr/bin/apt-get", Args: []string{"-o", lockTimeout, "update"}}

func aptInstall(v contract.PHPVersion) sysexec.Script {
	args := []string{"-o", lockTimeout, "-o", "Dpkg::Options::=--force-confdef", "-o", "Dpkg::Options::=--force-confold",
		"install", "-y", "--no-install-recommends"}
	return sysexec.Script{Path: "/usr/bin/apt-get", Args: append(args, Packages(v)...)}
}

func fpmTest(v contract.PHPVersion) sysexec.Script {
	return sysexec.Script{Path: "/usr/sbin/php-fpm" + string(v), Args: []string{"-t"}}
}

func systemctlCmd(args ...string) sysexec.Script {
	return sysexec.Script{Path: "/usr/bin/systemctl", Args: args}
}

// commands is each recorded call as one line, path and arguments.
func commands(calls []sysexec.Call) []string {
	lines := make([]string, len(calls))
	for i, c := range calls {
		lines[i] = strings.Join(append([]string{c.Path}, c.Args...), " ")
	}
	return lines
}

func line(s sysexec.Script) string { return strings.Join(append([]string{s.Path}, s.Args...), " ") }

// lastOf is the last command run, or "" if none ran.
func lastOf(lines []string) string {
	if len(lines) == 0 {
		return ""
	}
	return lines[len(lines)-1]
}
