package helper

import (
	"bytes"
	"strings"
	"testing"

	"zimam/agent/contract"
)

func TestAsUserRefusesRoot(t *testing.T) {
	for _, ids := range []IDs{
		{UID: 0, EUID: 0, GID: 0, EGID: 0},
		{UID: 1001, EUID: 0, GID: 1001, EGID: 1001},
		{UID: 1001, EUID: 1001, GID: 1001, EGID: 0},
		{UID: 0, EUID: 1001, GID: 1001, EGID: 1001},
		{UID: 1001, EUID: 1001, GID: 0, EGID: 1001},
		{UID: 1001, EUID: 1001, GID: 1001, EGID: 1001, Groups: []int{1001, 0}},
	} {
		var stdout, stderr bytes.Buffer
		code := Main(ids, []string{"--home", t.TempDir(), "stat", "."}, strings.NewReader(""), &stdout, &stderr)
		if code != 1 || stdout.Len() != 0 || !strings.Contains(stderr.String(), "must not run as root") {
			t.Errorf("Main(%+v) = %d, %q, %q; want 1 and a refusal", ids, code, stdout.String(), stderr.String())
		}
	}
}

func TestMainUsage(t *testing.T) {
	ids := IDs{UID: 1001, EUID: 1001, GID: 1001, EGID: 1001}
	for _, args := range [][]string{
		nil,
		{"--home"},
		{"--home", "/home/s_x"},
		{"--house", "/home/s_x", "stat", "."},
		{"--home", "home/s_x", "stat", "."},
		{"--home", "/home/s_x/../../root", "stat", "."},
	} {
		var stdout, stderr bytes.Buffer
		code := Main(ids, args, strings.NewReader(""), &stdout, &stderr)
		if code != 2 || stdout.Len() != 0 || !strings.Contains(stderr.String(), "usage") {
			t.Errorf("Main(%q) = %d, %q, %q; want 2 and usage", args, code, stdout.String(), stderr.String())
		}
	}
}

func TestServeUsage(t *testing.T) {
	root, _, _ := testHome(t)
	for _, c := range []struct {
		op   string
		args []string
	}{
		{"nope", nil},
		{"stat", nil},
		{"stat", []string{"a", "b"}},
		{"read", []string{"f", "x", "1"}},
		{"read", []string{"f", "0", "1.5"}},
		{"mkdir", []string{"d", "1777"}}, // more than permission bits
		{"mkdir", []string{"d", "rwx"}},
		{"write", []string{"f", "8"}},
		{"tail", []string{"f", "-1", "x"}},
		{"search", []string{"f", "0", "10", "bad\x01query"}},
		{"extract-tar", []string{"d", "-1", "10"}},
		{"extract-tar", []string{"d", "10", "-1"}},
	} {
		var stdout, stderr bytes.Buffer
		code := Serve(root, c.op, c.args, strings.NewReader(""), &stdout, &stderr)
		if code != 2 || stdout.Len() != 0 || stderr.Len() == 0 {
			t.Errorf("Serve(%s, %q) = %d, %q, %q; want 2 and a message", c.op, c.args, code, stdout.String(), stderr.String())
		}
	}
}

func TestServeExitCodes(t *testing.T) {
	root, home, _ := testHome(t)
	put(t, home, "f", "data")
	serve := func(op string, args ...string) (int, string, string) {
		var stdout, stderr bytes.Buffer
		code := Serve(root, op, args, strings.NewReader(""), &stdout, &stderr)
		return code, stdout.String(), stderr.String()
	}
	// Success prints the result as one JSON line.
	code, out, _ := serve("stat", "f")
	var fi FileInfo
	if code != 0 || strings.Count(out, "\n") != 1 || !strings.HasSuffix(out, "\n") ||
		contract.DecodeStrict([]byte(out), &fi) != nil || fi.Size != 4 {
		t.Errorf("stat f = %d, %q; want 0 and one JSON line", code, out)
	}
	// An operation without a result prints {}.
	if code, out, _ := serve("mkdir", "d", "750"); code != 0 || out != "{}\n" {
		t.Errorf("mkdir = %d, %q; want 0 and {}", code, out)
	}
	// A missing file exits 3; any other failure 1. Both explain on stderr.
	if code, out, errOut := serve("stat", "missing"); code != 3 || out != "" || errOut == "" {
		t.Errorf("stat missing = %d, %q, %q; want 3", code, out, errOut)
	}
	if code, out, errOut := serve("read", "../f", "0", "1"); code != 1 || out != "" || errOut == "" {
		t.Errorf("read ../f = %d, %q, %q; want 1", code, out, errOut)
	}
}
