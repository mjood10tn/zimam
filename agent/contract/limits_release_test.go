//go:build !e2e

package contract

import "testing"

func TestReleaseMinSiteCap(t *testing.T) {
	if minSiteCapBytes != GiB {
		t.Errorf("minSiteCapBytes = %d, want 1 GiB in release builds", minSiteCapBytes)
	}
}
