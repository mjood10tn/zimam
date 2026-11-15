package siteexec

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"zimam/agent/contract"
	"zimam/agent/internal/helper"
	"zimam/agent/internal/sysexec"
)

func TestProgramPath(t *testing.T) {
	for _, c := range []struct {
		p    Program
		php  contract.PHPVersion
		want string
	}{
		{Git, "", "/usr/bin/git"},
		{SSHKeygen, "", "/usr/bin/ssh-keygen"},
		{DeployScript, "", "/bin/bash"},
		{PHP, "8.3", "/usr/bin/php8.3"},
		{Artisan, "8.4", "/usr/bin/php8.4"},
		{Composer, "8.5", "/usr/bin/php8.5"},
	} {
		if got, err := c.p.Path(c.php); err != nil || got != c.want {
			t.Errorf("%s.Path(%q) = %q, %v; want %q", c.p, c.php, got, err, c.want)
		}
	}
	for _, c := range []struct {
		p   Program
		php contract.PHPVersion
	}{{PHP, ""}, {Composer, "7.4"}, {Artisan, "8.3/../x"}, {"curl", ""}, {"", ""}} {
		if got, err := c.p.Path(c.php); err == nil {
			t.Errorf("%q.Path(%q) = %q, nil; want an error", c.p, c.php, got)
		}
	}
}

func TestProgramsAreTheFixedSet(t *testing.T) {
	want := []Program{Git, Composer, PHP, Artisan, SSHKeygen, DeployScript}
	if fmt.Sprint(Programs) != fmt.Sprint(want) {
		t.Errorf("Programs = %v, want %v", Programs, want)
	}
	names := []string{"git", "composer", "php", "artisan", "ssh-keygen", "deploy-script"}
	for i, p := range want {
		if string(p) != names[i] {
			t.Errorf("program %d is %q, want %q", i, p, names[i])
		}
	}
}

// Run refuses a bad request before it starts the helper: the client's agent
// binary doesn't exist, so anything that got that far fails differently.
func TestRunRefusesBadRequests(t *testing.T) {
	h := helper.Client{AgentBinary: "/nonexistent/zimam-agent"}
	a := helper.Account{Name: "s_shop", UID: 1001, GID: 1001, Home: "/home/s_shop"}
	d := sysexec.Deps{Logf: func(string, ...any) {}}
	for _, r := range []Request{
		{Program: "curl"},
		{Program: Git, Env: map[string]string{"LD_PRELOAD": "x"}},
		{Program: Git, Env: map[string]string{"COMPOSER_HOME": "/home/s_shop/.composer"}},
		{Program: Git, Dir: "../s_other"},
		{Program: Git, Dir: "/etc"},
		{Program: Git, Stdin: strings.NewReader("x")},
		{Program: PHP},
		{Program: DeployScript, Args: []string{"-c", "id"}},
		{Program: Git, Capture: &bytes.Buffer{}},
	} {
		_, err := Run(context.Background(), d, h, a, r)
		if err == nil || strings.Contains(err.Error(), "nonexistent") {
			t.Errorf("Run(%+v) = %v; want a refusal before starting", r, err)
		}
	}
}

func TestLogLinesArePrefixed(t *testing.T) {
	var got []string
	d := sysexec.Deps{Logf: func(format string, args ...any) { got = append(got, fmt.Sprintf(format, args...)) }}
	log := logTo(d, "composer: ")
	log("Installing %s 100%") // a line is never a format string
	if len(got) != 1 || got[0] != "composer: Installing %s 100%" {
		t.Errorf("logged %q", got)
	}
}

func TestOutcome(t *testing.T) {
	if code, err := outcome(Git, 0, nil); code != 0 || err != nil {
		t.Errorf("outcome(0) = %d, %v", code, err)
	}
	code, err := outcome(Git, 128, nil)
	var exitErr *sysexec.ExitError
	if code != 128 || !errors.As(err, &exitErr) || exitErr.Code != 128 || !strings.Contains(err.Error(), "git") {
		t.Errorf("outcome(128) = %d, %v; want 128 and an ExitError naming git", code, err)
	}
	boom := errors.New("boom")
	if _, err := outcome(Git, 0, boom); !errors.Is(err, boom) {
		t.Errorf("outcome(boom) = %v, want boom", err)
	}
}
