package main

import (
	"errors"
	"os/user"
	"strings"
	"testing"

	"zimam/agent/internal/config"
	"zimam/agent/internal/paths"
)

func TestJobRunUsage(t *testing.T) {
	for _, args := range [][]string{
		{"job", "run"},
		{"job", "run", "not-a-ulid"},
		{"job", "run", "01JBX7Q3A1B2C3D4E5F6G7H8J9", "extra"},
	} {
		code, _, stderr := runCapture(args)
		if code != 2 || !strings.Contains(stderr, "usage") {
			t.Errorf("run(%q) = %d, %q; want 2 and usage", args, code, stderr)
		}
	}
}

func TestJobManagerHelper(t *testing.T) {
	p := paths.Paths{Root: "/tmp/zimam-test"}
	m := jobManager(p, config.Agent{}, -1)
	if m.Helper == nil || m.Helper.AgentBinary != p.AgentBinary() {
		t.Errorf("Helper = %+v, want one running %s", m.Helper, p.AgentBinary())
	}
}

func TestJobLogGID(t *testing.T) {
	lookup := func(g *user.Group, err error) func(string) (*user.Group, error) {
		return func(name string) (*user.Group, error) {
			if name != "zimam-agent" {
				t.Errorf("looked up %q, want zimam-agent", name)
			}
			return g, err
		}
	}
	if gid, err := jobLogGID(lookup(&user.Group{Gid: "998"}, nil)); gid != 998 || err != nil {
		t.Errorf("jobLogGID = %d, %v; want 998", gid, err)
	}
	if gid, err := jobLogGID(lookup(nil, user.UnknownGroupError("zimam-agent"))); gid != -1 || err != nil {
		t.Errorf("without the group: %d, %v; want -1, nil", gid, err)
	}
	if _, err := jobLogGID(lookup(nil, errors.New("reading /etc/group failed"))); err == nil {
		t.Error("a lookup failure must stop the job")
	}
}
