package valkey

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"zimam/agent/internal/sysexec"
)

const testPass = "k9Qm2ZxL0vB7tR4nYc8WdE1aHs5UjP3fGoIq6XyTz_A"

func cliEnv(t *testing.T) *testEnv {
	t.Helper()
	e := newEnv(t)
	if err := os.MkdirAll(filepath.Dir(e.p.ValkeyAdminPass()), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(e.p.ValkeyAdminPass(), []byte(testPass+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return e
}

func TestCliRunsAsTheAdminWithThePasswordInTheEnvironment(t *testing.T) {
	e := cliEnv(t)
	e.add(live("PONG\n", "PING"))
	out, err := Cli(context.Background(), e.d, "PING")
	if err != nil || out != "PONG" {
		t.Fatalf("Cli = %q, %v", out, err)
	}
	calls := e.run.Calls()
	if len(calls) != 1 {
		t.Fatalf("ran %d commands", len(calls))
	}
	c := calls[0]
	if c.Path != "/usr/bin/valkey-cli" {
		t.Errorf("ran %s", c.Path)
	}
	if want := []string{"-e", "--no-auth-warning", "--user", "zimam-admin", "-h", "127.0.0.1", "-p", "6379", "PING"}; !slices.Equal(c.Args, want) {
		t.Errorf("args = %q, want %q", c.Args, want)
	}
	if want := []string{"REDISCLI_AUTH=" + testPass}; !slices.Equal(c.Env, want) {
		t.Errorf("env = %q, want only REDISCLI_AUTH", c.Env)
	}
	if strings.Contains(strings.Join(c.Args, " "), testPass) || len(c.Stdin) != 0 {
		t.Error("the password is in the argv or stdin")
	}
}

// valkey-cli answers some errors with exit 0, and a command that fails is
// not a command that worked.
func TestCliErrorRepliesAreErrors(t *testing.T) {
	for _, reply := range []string{
		"ERR unknown command 'FOO'\n",
		"NOPERM User s_a has no permissions to run the 'config|get' command\n",
		"WRONGPASS invalid username-password pair or user is disabled.\n",
		"NOAUTH Authentication required.\n",
		"(error) ERR syntax error\n",
		"LOADING Valkey is loading the dataset in memory\n",
		"ERR /etc/zimam/valkey/users.acl:3: Error in applying operation '+@nosuchcat'\nWARNING: ACL errors detected\n",
	} {
		e := cliEnv(t)
		e.add(live(reply, "ACL", "LOAD"))
		out, err := Cli(context.Background(), e.d, "ACL", "LOAD")
		if err == nil {
			t.Errorf("Cli accepted %q (output %q)", reply, out)
			continue
		}
		if !strings.Contains(err.Error(), strings.Fields(reply)[0]) && !strings.Contains(err.Error(), "ERR") {
			t.Errorf("the error %q doesn't carry the reply %q", err, reply)
		}
	}
	for _, reply := range []string{"OK\n", "PONG\n", "1\n", "0\n", liveConfig, "# Server\r\nredis_version:7.2.4\r\n", "user default off sanitize-payload\n", "\n"} {
		e := cliEnv(t)
		e.add(live(reply, "X"))
		if _, err := Cli(context.Background(), e.d, "X"); err != nil {
			t.Errorf("Cli refused %q: %v", reply, err)
		}
	}
}

func TestCliFailureNeverQuotesThePassword(t *testing.T) {
	e := cliEnv(t)
	s := live("", "PING")
	s.Result = sysexec.Result{ExitCode: 1, Stdout: []byte("AUTH failed: WRONGPASS (sent " + testPass + ")\n"), Stderr: []byte("Could not connect, tried " + testPass + "\n")}
	e.add(s)
	_, err := Cli(context.Background(), e.d, "PING")
	if err == nil {
		t.Fatal("Cli ignored the exit code")
	}
	if strings.Contains(err.Error(), testPass) {
		t.Errorf("the error quotes the password: %v", err)
	}
	if !strings.Contains(err.Error(), "WRONGPASS") {
		t.Errorf("the error lost the reason: %v", err)
	}
}

func TestCliWithoutAnAdminPassword(t *testing.T) {
	e := newEnv(t)
	if _, err := Cli(context.Background(), e.d, "PING"); err == nil {
		t.Fatal("Cli ran with no admin password")
	}
	if n := len(e.run.Calls()); n != 0 {
		t.Errorf("ran %d commands", n)
	}
}

func TestCliReachesTheCandidateByItsSocket(t *testing.T) {
	e := cliEnv(t)
	e.add(sysexec.Script{Path: cliPath, Args: []string{"-e", "--no-auth-warning", "--user", "zimam-admin", "-s", "/run/x/s.sock", "PING"}, Result: sysexec.Result{Stdout: []byte("PONG\n")}})
	if out, err := cliSocket(context.Background(), e.d, "/run/x/s.sock", "PING"); err != nil || out != "PONG" {
		t.Fatalf("cliSocket = %q, %v", out, err)
	}
}
