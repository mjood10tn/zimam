//go:build e2e

package config

import (
	"reflect"
	"testing"
)

// An e2e build loads e2e_git_host, and a Save keeps it: logs.policy and
// php.default rewrite agent.json during a run.
func TestE2ELoadsGitHost(t *testing.T) {
	p := writeWithGitHost(t)
	a, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(a.E2EGitHost, testGitHost()) {
		t.Fatalf("E2EGitHost = %+v, want %+v", a.E2EGitHost, testGitHost())
	}
	if err := Save(p, a); err != nil {
		t.Fatal(err)
	}
	again, err := Load(p)
	if err != nil || !reflect.DeepEqual(again.E2EGitHost, testGitHost()) {
		t.Fatalf("after Save: %+v, %v", again.E2EGitHost, err)
	}
	a.E2EGitHost.Address = "githost"
	if err := Save(p, a); err == nil {
		t.Fatal("Save wrote a Git host given by name")
	}
}
