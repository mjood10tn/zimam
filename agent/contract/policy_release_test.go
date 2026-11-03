//go:build !e2e

package contract

import "testing"

// Release builds keep §5.3's floor of 1 GiB for the cap; e2e builds lower
// it for the maintenance probe.
func TestPolicyCapFloorInRelease(t *testing.T) {
	p := LogsPolicyParams{RetentionDays: 14, AuditRetentionDays: 365, SiteCapBytes: SiteCapBytes(GiB - 1)}
	if p.Validate() == nil {
		t.Error("a cap under 1 GiB passed")
	}
}
