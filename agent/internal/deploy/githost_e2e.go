//go:build e2e

package deploy

import (
	"strconv"
	"strings"

	"zimam/agent/internal/config"
)

// gitSSHCommand is the SSH command git fetches with. In an e2e build with
// e2e_git_host set, git still asks for git@github.com, but ssh connects to
// the run's own Git host (e2e/githost) and checks its keys under the name
// github.com: everything else, the URL and the pinning options included, is
// what a release build runs. The address is an IP literal and the port a
// number (config.E2EGitHost), so the string /bin/sh reads holds no other
// text.
func gitSSHCommand(c config.Agent) string {
	h := c.E2EGitHost
	if h == nil {
		return GitSSHCommand
	}
	return GitSSHCommand + " -o HostKeyAlias=github.com -o HostName=" + h.Address + " -o Port=" + strconv.Itoa(h.Port)
}

// pinnedHosts is what site.repo writes to the site's known_hosts: GitHub's
// keys, or the e2e Git host's under the name github.com.
func pinnedHosts(c config.Agent) string {
	h := c.E2EGitHost
	if h == nil {
		return GitHubKnownHosts
	}
	var b strings.Builder
	for _, k := range h.HostKeys {
		b.WriteString("github.com " + k + "\n")
	}
	return b.String()
}
