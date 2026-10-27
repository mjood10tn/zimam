//go:build e2e

package contract

import "testing"

func TestE2EMinSiteCap(t *testing.T) {
	if minSiteCapBytes != 1024 {
		t.Errorf("minSiteCapBytes = %d, want 1 KiB in e2e builds", minSiteCapBytes)
	}
}
