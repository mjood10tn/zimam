package commands

import (
	"testing"

	"zimam/agent/internal/jobs"
)

func TestNewAddsJobCommands(t *testing.T) {
	if h := New(nil); len(h) != 1 || h["agent.version"] == nil {
		t.Errorf("New(nil) has %d handlers, want only agent.version", len(h))
	}
	m := &jobs.Manager{Handlers: map[string]jobs.Handler{"php.default": nil}}
	h := New(m)
	for _, name := range []string{"agent.version", "job.status", "job.follow", "job.cancel", "php.default"} {
		if h[name] == nil {
			t.Errorf("New(m) has no handler for %s", name)
		}
	}
}
