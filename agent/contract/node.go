package contract

import (
	"errors"
	"time"
)

// The Node commands (R04-D6): one NodeSource major per server.
func init() {
	Register(Descriptor{
		Name:    "node.install",
		Kind:    KindJob,
		Audited: true,
		Params:  func() Params { return &NodeInstallParams{} },
		Result:  NodeInstallResult{},
		Subject: func(Params) string { return "server" },
	})
	Register(Descriptor{
		Name:    "node.uninstall",
		Kind:    KindJob,
		Audited: true,
		Params:  func() Params { return &NodeUninstallParams{} },
		Result:  NodeUninstallResult{},
		Subject: func(Params) string { return "server" },
	})
}

type NodeRelease struct {
	Major     NodeMajor
	EndOfLife string // last day of support (nodejs.org), YYYY-MM-DD
}

// NodeAllowlist is OQ5: the two majors NodeSource builds for Ubuntu 26.04
// that are supported (spike results, 2026-11-18). A major joins when Node
// publishes it as LTS.
var NodeAllowlist = []NodeRelease{
	{"24", "2028-04-30"},
	{"26", "2029-04-30"},
}

// DefaultNodeMajor is what node.install installs without a major.
const DefaultNodeMajor NodeMajor = "26"

type NodeMajor string

func (m NodeMajor) Release() (NodeRelease, bool) {
	for _, r := range NodeAllowlist {
		if r.Major == m {
			return r, true
		}
	}
	return NodeRelease{}, false
}

func (m NodeMajor) Validate() error {
	if _, ok := m.Release(); !ok {
		return errors.New("is not a supported Node major version")
	}
	return nil
}

func (NodeMajor) JSONSchema() Schema {
	majors := make([]NodeMajor, len(NodeAllowlist))
	for i, r := range NodeAllowlist {
		majors[i] = r.Major
	}
	return Schema{"type": "string", "enum": majors}
}

// SupportedOn reports whether r is still supported on the given day: through
// its end date, not after it.
func (r NodeRelease) SupportedOn(now time.Time) bool {
	end, _ := time.Parse(time.DateOnly, r.EndOfLife)
	return now.UTC().Before(end.AddDate(0, 0, 1))
}

// NodeInstallParams is node.install's params: the major to install, the
// default one without. Another major than the installed one replaces it.
type NodeInstallParams struct {
	Major NodeMajor `json:"major,omitempty"`
}

func (p *NodeInstallParams) Validate() error {
	f := FieldErrors{}
	if p.Major != "" {
		f.Check("major", p.Major)
	}
	return f.Err()
}

// Chosen is the major with the default applied.
func (p NodeInstallParams) Chosen() NodeMajor {
	if p.Major == "" {
		return DefaultNodeMajor
	}
	return p.Major
}

// NodeInstallResult is the major installed and the version its package
// reports.
type NodeInstallResult struct {
	Major   NodeMajor `json:"major"`
	Version string    `json:"version"`
}

// NodeUninstallParams is node.uninstall's params: none.
type NodeUninstallParams struct{}

func (NodeUninstallParams) Validate() error { return nil }

// NodeUninstallResult is node.uninstall's answer: nothing to report.
type NodeUninstallResult struct{}
