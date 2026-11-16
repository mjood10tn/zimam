//go:build !e2e

package config

import "errors"

// Validate refuses any Git host: only e2e builds may fetch from anywhere
// but GitHub, with any host keys but GitHub's pinned ones. Load then
// refuses the whole agent.json, so a release agent never runs with it.
func (h E2EGitHost) Validate() error {
	return errors.New("only e2e builds accept e2e_git_host")
}
