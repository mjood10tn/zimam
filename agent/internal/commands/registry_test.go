package commands

import (
	"testing"

	"zimam/agent/internal/jobs"
	"zimam/agent/internal/sysexec"
)

func TestNewAddsJobCommands(t *testing.T) {
	if h := New(sysexec.Deps{}, nil); len(h) != 5 || h["agent.version"] == nil || h["system.status"] == nil || h["system.metrics"] == nil || h["logs.list"] == nil || h["logs.read"] == nil {
		t.Errorf("New(d, nil) has %d handlers, want only the reads: agent.version, system.status, system.metrics, logs.list and logs.read", len(h))
	}
	m := &jobs.Manager{Handlers: map[string]jobs.Handler{"php.default": nil}}
	h := New(sysexec.Deps{}, m)
	for _, name := range []string{"agent.version", "system.status", "system.metrics", "job.status", "job.follow", "job.cancel", "php.default"} {
		if h[name] == nil {
			t.Errorf("New(d, m) has no handler for %s", name)
		}
	}
}
