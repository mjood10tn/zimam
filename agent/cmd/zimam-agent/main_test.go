package main

import (
	"bytes"
	"strings"
	"testing"
)

// The command-line subcommands from design §5.3.
var wantSubcommands = []string{
	"serve",
	"bootstrap",
	"panel setup-link",
	"hook cert-deployed",
	"maintenance",
	"job run",
	"as-user",
	"version", // D25: the installer's repair check (M1 review, M5)
}

func runCapture(args []string) (code int, stdout, stderr string) {
	var out, errOut bytes.Buffer
	code = run(args, strings.NewReader(""), &out, &errOut)
	return code, out.String(), errOut.String()
}

func TestUsageListsSubcommands(t *testing.T) {
	code, stdout, stderr := runCapture(nil)
	if code != 2 {
		t.Errorf("run() = %d, want 2", code)
	}
	if stdout != "" {
		t.Errorf("stdout = %q, want empty", stdout)
	}
	for _, name := range wantSubcommands {
		if !strings.Contains(stderr, name) {
			t.Errorf("usage does not name %q:\n%s", name, stderr)
		}
	}
}

func TestUnknownSubcommand(t *testing.T) {
	for _, args := range [][]string{
		{"nope"},
		{"panel"},
		{"panel", "nope"},
		{"hook"},
		{"job"},
		{"panel setup-link"},
	} {
		code, _, stderr := runCapture(args)
		if code != 2 {
			t.Errorf("run(%q) = %d, want 2", args, code)
		}
		if !strings.Contains(stderr, "usage") {
			t.Errorf("run(%q) did not print usage:\n%s", args, stderr)
		}
	}
}
