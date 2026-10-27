//go:build e2e

package contract

// minSiteCapBytes is lowered in e2e builds so the maintenance probe can set a
// tiny cap (§14.4).
var minSiteCapBytes = int64(1024)
