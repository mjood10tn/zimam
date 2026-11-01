package main

import (
	"strings"
	"testing"
)

// maintenance takes no arguments; with one it changes nothing and says how
// to run it.
func TestMaintenanceUsage(t *testing.T) {
	code, _, stderr := runCapture([]string{"maintenance", "extra"})
	if code != 2 || !strings.Contains(stderr, "usage: zimam-agent maintenance") {
		t.Errorf("exit %d, stderr %q; want 2 and usage", code, stderr)
	}
}
