//go:build !e2e

package deploy

import (
	"testing"

	"zimam/agent/internal/config"
)

// A release build reaches GitHub with GitHub's pinned keys whatever the
// config holds; config.Load refuses e2e_git_host there anyway.
func TestReleaseAlwaysGitHub(t *testing.T) {
	c := config.Agent{E2EGitHost: &config.E2EGitHost{Address: "127.0.0.1", Port: 2222, HostKeys: []string{"ssh-ed25519 AAAA"}}}
	if got := gitSSHCommand(c); got != GitSSHCommand {
		t.Errorf("gitSSHCommand = %q, want GitHub's", got)
	}
	if got := pinnedHosts(c); got != GitHubKnownHosts {
		t.Errorf("pinnedHosts = %q, want GitHub's keys", got)
	}
}
