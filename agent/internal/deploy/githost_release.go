//go:build !e2e

package deploy

import "zimam/agent/internal/config"

// gitSSHCommand is the SSH command git fetches with. A release build always
// reaches GitHub; config refuses e2e_git_host there.
func gitSSHCommand(config.Agent) string { return GitSSHCommand }

// pinnedHosts is what site.repo writes to the site's known_hosts: GitHub's
// keys, in a release build always.
func pinnedHosts(config.Agent) string { return GitHubKnownHosts }
