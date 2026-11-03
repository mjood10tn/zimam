package contract

import (
	"errors"
	"slices"
	"time"
)

func init() {
	Register(Descriptor{
		Name:    "security.check",
		Kind:    KindJob,
		Audited: true,
		Params:  func() Params { return &SecurityCheckParams{} },
		Result:  SecurityCheckResult{},
		Subject: func(p Params) string {
			sp := p.(*SecurityCheckParams)
			if sp.Scope == CheckScopeSite {
				return "site:" + string(sp.Name)
			}
			return "server"
		},
	})
}

// CheckScope is what security.check looks at (§9.7).
type CheckScope string

const (
	CheckScopeServer CheckScope = "server"
	CheckScopeSite   CheckScope = "site"
)

var checkScopes = []CheckScope{CheckScopeServer, CheckScopeSite}

func (s CheckScope) Validate() error {
	if !slices.Contains(checkScopes, s) {
		return errors.New("must be server or site")
	}
	return nil
}

func (CheckScope) JSONSchema() Schema { return Schema{"type": "string", "enum": checkScopes} }

// SecurityCheckParams is security.check's params (§5.3): the server, or one
// site by name.
type SecurityCheckParams struct {
	Scope CheckScope `json:"scope"`
	Name  SiteName   `json:"name,omitempty"`
}

func (p *SecurityCheckParams) Validate() error {
	f := FieldErrors{}
	f.Check("scope", p.Scope)
	switch {
	case p.Scope == CheckScopeSite && p.Name == "":
		f["name"] = "is required for a site's checks"
	case p.Scope == CheckScopeSite:
		f.Check("name", p.Name)
	case p.Name != "":
		f["name"] = "must be left out for the server's checks"
	}
	return f.Err()
}

// FindingStatus is one check's result (§7.6).
type FindingStatus string

const (
	FindingPass         FindingStatus = "pass"
	FindingWarn         FindingStatus = "warn"
	FindingFail         FindingStatus = "fail"
	FindingInconclusive FindingStatus = "inconclusive"
)

var findingStatuses = []FindingStatus{FindingPass, FindingWarn, FindingFail, FindingInconclusive}

func (s FindingStatus) Validate() error {
	if !slices.Contains(findingStatuses, s) {
		return errors.New("must be pass, warn, fail or inconclusive")
	}
	return nil
}

func (FindingStatus) JSONSchema() Schema { return Schema{"type": "string", "enum": findingStatuses} }

// Finding is one check's outcome. Detail says what was found, in English;
// ExpiresAt is set by certificate_valid only, which is how the panel learns
// about renewals (§10).
type Finding struct {
	Check     string        `json:"check"`
	Status    FindingStatus `json:"status"`
	Detail    string        `json:"detail"`
	ExpiresAt *time.Time    `json:"expires_at"`
}

// SecurityCheckResult is every check of the scope, in §9.7's order.
type SecurityCheckResult struct {
	Scope    CheckScope `json:"scope"`
	Name     SiteName   `json:"name,omitempty"`
	Findings []Finding  `json:"findings"`
}
