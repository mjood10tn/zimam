package sysexec

import (
	"context"
	"fmt"
	"io"
	"slices"
	"sync"
)

// Script is one scripted answer for FakeRunner. Scripts for the same Path
// and Args answer in order; the last one then repeats.
type Script struct {
	Path   string
	Args   []string
	Result Result
	Err    error // returned as is; otherwise a non-zero ExitCode gives an *ExitError
}

// Call is one recorded Run, with everything the caller wrote to Stdin.
type Call struct {
	Path  string
	Args  []string
	Env   []string
	Dir   string
	Stdin []byte
}

// FakeRunner is a Runner for tests. It matches calls by Path plus Args,
// returns the scripted results, and records every call, so tests can check
// where secrets went. An unscripted call fails.
type FakeRunner struct {
	mu      sync.Mutex
	scripts []Script
	used    map[int]bool
	calls   []Call
}

func NewFakeRunner(scripts ...Script) *FakeRunner {
	return &FakeRunner{scripts: scripts, used: map[int]bool{}}
}

// Add scripts another answer.
func (f *FakeRunner) Add(s Script) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.scripts = append(f.scripts, s)
}

// Calls returns the calls recorded so far, in order.
func (f *FakeRunner) Calls() []Call {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.calls)
}

func (f *FakeRunner) Run(ctx context.Context, c Cmd) (Result, error) {
	var stdin []byte
	if c.Stdin != nil {
		var err error
		if stdin, err = io.ReadAll(c.Stdin); err != nil {
			return Result{}, err
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, Call{Path: c.Path, Args: slices.Clone(c.Args), Env: slices.Clone(c.Env), Dir: c.Dir, Stdin: stdin})
	if err := checkPath(c.Path); err != nil {
		return Result{}, err
	}
	last := -1
	for i, s := range f.scripts {
		if s.Path != c.Path || !slices.Equal(s.Args, c.Args) {
			continue
		}
		last = i
		if !f.used[i] {
			break
		}
	}
	if last < 0 {
		return Result{}, fmt.Errorf("FakeRunner: unscripted command %s %q", c.Path, c.Args)
	}
	f.used[last] = true
	s := f.scripts[last]
	if s.Err != nil {
		return s.Result, s.Err
	}
	if s.Result.ExitCode != 0 {
		return s.Result, &ExitError{Path: c.Path, Code: s.Result.ExitCode, Stderr: s.Result.Stderr}
	}
	return s.Result, nil
}
