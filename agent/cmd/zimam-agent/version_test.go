package main

import (
	"testing"

	"zimam/agent/internal/release"
)

// version prints the agent's release; --at-least lets the installer refuse
// to move a server to an older release (M1 review, M5).
func TestVersion(t *testing.T) {
	old := release.Version
	release.Version = "1.2.3"
	t.Cleanup(func() { release.Version = old })

	if code, stdout, _ := runCapture([]string{"version"}); code != 0 || stdout != "1.2.3\n" {
		t.Errorf("version = %d, %q; want 0 and 1.2.3", code, stdout)
	}
	for _, tc := range []struct {
		other string
		code  int
	}{
		{"1.2.3", 0},
		{"1.2.2", 0},
		{"1.0.10", 0},
		{"1.2.3-rc.1", 0}, // a pre-release comes before its release
		{"1.2.4", 1},
		{"1.10.0", 1}, // compared as numbers, not text
		{"2.0.0-rc.1", 1},
		{"v1.2.3", 2},
		{"", 2},
	} {
		code, stdout, stderr := runCapture([]string{"version", "--at-least", tc.other})
		if code != tc.code {
			t.Errorf("version --at-least %q = %d (%q, %q), want %d", tc.other, code, stdout, stderr, tc.code)
		}
	}
	if code, _, _ := runCapture([]string{"version", "extra"}); code != 2 {
		t.Errorf("version extra = %d, want 2", code)
	}
}
