package main

import (
	"os"
	"strings"
	"testing"
)

func TestAsUser(t *testing.T) {
	code, stdout, stderr := runCapture([]string{"as-user"})
	if os.Geteuid() == 0 {
		// Run by root, without the credential switch, as-user refuses first.
		if code != 1 || stdout != "" || !strings.Contains(stderr, "must not run as root") {
			t.Errorf("run(as-user) as root = %d, %q, %q; want 1 and a refusal", code, stdout, stderr)
		}
		return
	}
	if code != 2 || stdout != "" || !strings.Contains(stderr, "usage") {
		t.Errorf("run(as-user) = %d, %q, %q; want 2 and usage", code, stdout, stderr)
	}
}
