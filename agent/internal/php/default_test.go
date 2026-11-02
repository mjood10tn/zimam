package php

import (
	"context"
	"slices"
	"testing"

	"zimam/agent/internal/sysexec"
)

func alternatives(args ...string) sysexec.Script {
	return sysexec.Script{Path: "/usr/bin/update-alternatives", Args: args}
}

func TestSetDefaultSetsAlternatives(t *testing.T) {
	e := newEnv(t, nil, "/php/")
	for _, s := range []sysexec.Script{
		alternatives("--set", "php", "/usr/bin/php8.3"),
		alternatives("--query", "phar"),
		alternatives("--set", "phar", "/usr/bin/phar8.3"),
		alternatives("--query", "phar.phar"),
		alternatives("--set", "phar.phar", "/usr/bin/phar.phar8.3"),
	} {
		e.run.Add(s)
	}
	if err := SetDefault(context.Background(), e.d, "8.3"); err != nil {
		t.Fatal(err)
	}
	var sets [][]string
	for _, c := range e.run.Calls() {
		if c.Args[0] == "--set" {
			sets = append(sets, c.Args)
		}
	}
	want := [][]string{{"--set", "php", "/usr/bin/php8.3"}, {"--set", "phar", "/usr/bin/phar8.3"}, {"--set", "phar.phar", "/usr/bin/phar.phar8.3"}}
	if !slices.EqualFunc(sets, want, slices.Equal) {
		t.Fatalf("update-alternatives --set calls = %q, want %q", sets, want)
	}
}

func TestSetDefaultSkipsUnregisteredPhar(t *testing.T) {
	e := newEnv(t, nil, "/php/")
	notRegistered := sysexec.Result{ExitCode: 2, Stderr: []byte("update-alternatives: error: no alternatives for phar")}
	for _, s := range []sysexec.Script{
		alternatives("--set", "php", "/usr/bin/php8.5"),
		{Path: "/usr/bin/update-alternatives", Args: []string{"--query", "phar"}, Result: notRegistered},
		{Path: "/usr/bin/update-alternatives", Args: []string{"--query", "phar.phar"}, Result: notRegistered},
	} {
		e.run.Add(s)
	}
	if err := SetDefault(context.Background(), e.d, "8.5"); err != nil {
		t.Fatal(err)
	}
	if n := len(e.run.Calls()); n != 3 {
		t.Fatalf("%d calls, want 3 (one --set, two --query): %+v", n, e.run.Calls())
	}
}

func TestSetDefaultRefusesUnknownVersion(t *testing.T) {
	e := newEnv(t, nil, "/php/")
	if err := SetDefault(context.Background(), e.d, "7.4"); err == nil {
		t.Fatal("SetDefault accepted PHP 7.4")
	}
	if len(e.run.Calls()) != 0 {
		t.Fatalf("ran %+v for a refused version", e.run.Calls())
	}
}
