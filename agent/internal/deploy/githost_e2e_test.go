//go:build e2e

package deploy

import (
	"testing"

	"zimam/agent/internal/config"
)

const e2eHostKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMqqnkVzrm0SdG6UOoqKLsabgH5C9okWi0dh2l9GKJl"

// Without e2e_git_host an e2e build reaches GitHub exactly as a release
// build does.
func TestE2EWithoutGitHostIsGitHub(t *testing.T) {
	if got := gitSSHCommand(config.Agent{}); got != GitSSHCommand {
		t.Errorf("gitSSHCommand = %q", got)
	}
	if got := pinnedHosts(config.Agent{}); got != GitHubKnownHosts {
		t.Errorf("pinnedHosts = %q", got)
	}
}

// With e2e_git_host, ssh connects to the run's Git host and checks its keys
// as github.com's; the pinning options stay.
func TestE2EGitHostOverride(t *testing.T) {
	c := config.Agent{E2EGitHost: &config.E2EGitHost{Address: "127.0.0.1", Port: 2222, HostKeys: []string{e2eHostKey}}}
	want := GitSSHCommand + " -o HostKeyAlias=github.com -o HostName=127.0.0.1 -o Port=2222"
	if got := gitSSHCommand(c); got != want {
		t.Errorf("gitSSHCommand = %q, want %q", got, want)
	}
	if got := pinnedHosts(c); got != "github.com "+e2eHostKey+"\n" {
		t.Errorf("pinnedHosts = %q", got)
	}
}
