//go:build !e2e

package config

import (
	"errors"
	"testing"

	"zimam/agent/contract"
)

// A release build refuses an agent.json with e2e_git_host, so a production
// agent can't be pointed at another Git host or other host keys (Task 12).
func TestReleaseRefusesE2EGitHost(t *testing.T) {
	p := writeWithGitHost(t)
	_, err := Load(p)
	var ae *contract.AgentError
	if !errors.As(err, &ae) || ae.Fields["e2e_git_host"] != "only e2e builds accept e2e_git_host" {
		t.Fatalf("Load = %v, want a refusal of e2e_git_host", err)
	}
	a := testAgent()
	a.E2EGitHost = testGitHost()
	if err := Save(p, a); err == nil {
		t.Fatal("Save wrote an agent.json with e2e_git_host")
	}
}
