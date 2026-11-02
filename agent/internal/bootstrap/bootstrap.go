// Package bootstrap is `zimam-agent bootstrap` (design §4): the eleven
// steps that turn a server prepared by install.sh into a working panel.
// Each step checks whether it is already done, so running bootstrap again
// converges, and each applied step is audited as cli:root.
package bootstrap

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"io"
	"io/fs"
	"time"

	"zimam/agent/contract"
	"zimam/agent/internal/audit"
	"zimam/agent/internal/config"
	"zimam/agent/internal/helper"
	"zimam/agent/internal/release"
	"zimam/agent/internal/sysexec"
)

// Helper is the part of the helper client bootstrap uses. Root never opens
// a path under /home/zimam itself (D5): every file there goes through it,
// running as zimam. *helper.Client is the real one.
type Helper interface {
	Mkdir(ctx context.Context, a helper.Account, name string, mode fs.FileMode) error
	Write(ctx context.Context, a helper.Account, name string, mode fs.FileMode, content io.Reader) error
	Stat(ctx context.Context, a helper.Account, name string) (helper.FileInfo, error)
	SymlinkSwap(ctx context.Context, a helper.Account, link, target string) error
	RemoveTree(ctx context.Context, a helper.Account, name string) error
	ExtractTar(ctx context.Context, a helper.Account, dest string, tarball io.Reader, lim helper.ExtractLimits) error
}

// IDs are the owners of Zimam's directories (§5.9): root, and the group
// zimam-agent, which install.sh creates.
type IDs struct {
	RootUID, RootGID int
	AgentGID         int
}

// Bootstrap holds what the steps need. The release fields come from the
// build (release.PublicKey, BaseURL, Version); tests set their own.
type Bootstrap struct {
	Deps         sysexec.Deps // Config is set by the agent-config step
	Helper       Helper
	Audit        audit.Writer
	Zimam        helper.Account
	IDs          IDs
	PublicKey    ed25519.PublicKey
	BaseURL      string
	AgentVersion string
}

// Options are install.sh's flags (§4). Empty values keep what agent.json
// already says on a re-run, or take the defaults on a first run.
type Options struct {
	PanelDomain  contract.Domain
	Email        string
	PHP          contract.PHPVersion
	Version      string // the release to install: install.sh passes the one it verified
	ACMEServer   string
	ACMECABundle string
}

// Step is one §4 step. Done says whether there is nothing left to do;
// Apply does it.
type Step struct {
	Name  string
	Done  func(ctx context.Context) (bool, error)
	Apply func(ctx context.Context) error
}

// StepError is a step that failed, with a hint for the person running the
// installer.
type StepError struct {
	Step string
	Err  error
	Hint string
}

func (e *StepError) Error() string { return fmt.Sprintf("step %s failed: %v", e.Step, e.Err) }
func (e *StepError) Unwrap() error { return e.Err }

// run is one bootstrap run: the options, and what the steps learn as they
// go.
type run struct {
	*Bootstrap
	o        Options
	cfg      config.Agent
	manifest *release.Manifest
	link     string
}

// StepNames lists the steps in the order they run, §4's.
func StepNames() []string {
	var names []string
	for _, s := range (&run{Bootstrap: &Bootstrap{}}).steps() {
		names = append(names, s.Name)
	}
	return names
}

// Run applies every step that isn't done, in order, and returns the setup
// link. It stops at the first failure: running it again resumes there.
func (b *Bootstrap) Run(ctx context.Context, o Options) (string, error) {
	if o.Version == "" {
		o.Version = b.AgentVersion
	}
	if !release.ValidVersion(o.Version) {
		return "", fmt.Errorf("release %q is not a SemVer version", o.Version)
	}
	r := &run{Bootstrap: b, o: o}
	// The audit log and its lock come first, so every step can be audited.
	if err := ensureDirs(r.auditDirs()); err != nil {
		return "", &StepError{Step: "directories", Err: err, Hint: hint("directories", o)}
	}
	for _, s := range r.steps() {
		start := b.Deps.Now()
		done, err := s.Done(ctx)
		if err == nil && done {
			b.Deps.Logf("== %s: already done", s.Name)
			continue
		}
		if err == nil {
			b.Deps.Logf("== %s", s.Name)
			err = s.Apply(ctx)
		}
		// A step that failed its check is audited too: it was asked for.
		outcome := "ok"
		if err != nil {
			outcome = "error:" + string(contract.ErrInternal)
		}
		if aerr := r.audit(s.Name, outcome, start); aerr != nil && err == nil {
			err = aerr
		}
		if err != nil {
			return "", &StepError{Step: s.Name, Err: err, Hint: hint(s.Name, o)}
		}
	}
	return r.link, nil
}

// audit writes one cli:root entry for an applied step. Its params name the
// step only: never the setup link, never .env's content.
func (r *run) audit(step, outcome string, start time.Time) error {
	return r.Audit.Write(audit.Entry{
		TS:      start,
		Actor:   "cli:root",
		Command: "bootstrap",
		Params: audit.Redact(struct {
			Step string `json:"step"`
		}{step}),
		Outcome:    outcome,
		DurationMS: r.Deps.Now().Sub(start).Milliseconds(),
	})
}
