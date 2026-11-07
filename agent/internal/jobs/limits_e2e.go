//go:build e2e

package jobs

// e2e.sleep's limits (D22), and e2e.memhog's, whose 512 MiB must end in
// oom-kill (§14.4). The commands exist only in e2e builds.
func init() {
	limits["e2e.sleep"] = Limits{MemoryMax: "64M", RuntimeMaxSec: 180}
	limits["e2e.memhog"] = Limits{MemoryMax: "64M", RuntimeMaxSec: 60}
}
