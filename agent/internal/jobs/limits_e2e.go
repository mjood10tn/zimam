//go:build e2e

package jobs

// e2e.sleep's limits (D22). The command exists only in e2e builds.
func init() {
	limits["e2e.sleep"] = Limits{MemoryMax: "64M", RuntimeMaxSec: 180}
}
