//go:build e2e

package config

// Validate accepts a well-formed Git host: e2e builds use it (Task 12).
func (h E2EGitHost) Validate() error {
	return h.check()
}
