package contract

import "time"

func init() {
	Register(Descriptor{
		Name:   "system.status",
		Kind:   KindRead,
		Params: func() Params { return &SystemStatusParams{} },
		Result: SystemStatusResult{},
	})
}

type SystemStatusParams struct{}

func (SystemStatusParams) Validate() error { return nil }

// SystemStatusResult is §7.5's system.status. Its slices are empty, never
// null: the schema expects arrays.
type SystemStatusResult struct {
	AgentVersion   string         `json:"agent_version"`
	Protocols      []int          `json:"protocols"`
	NginxVersion   string         `json:"nginx_version"`
	PHP            PHPStatus      `json:"php"`
	RebootRequired bool           `json:"reboot_required"` // /run/reboot-required exists
	Updates        UpdateCounts   `json:"updates"`
	PanelDomain    Domain         `json:"panel_domain"`
	ACMEDirectory  string         `json:"acme_directory"`
	Services       ServicesStatus `json:"services"`
}

// PHPStatus carries the allowlist and its end dates as well as what is
// installed, so the panel never hard-codes them (D19).
type PHPStatus struct {
	Installed []InstalledPHP `json:"installed"`
	Default   PHPVersion     `json:"default"`
	Panel     PHPVersion     `json:"panel"`
	Available []AvailablePHP `json:"available"`
}

type InstalledPHP struct {
	Version   PHPVersion `json:"version"`
	FPMActive bool       `json:"fpm_active"`
	InUse     bool       `json:"in_use"` // it has a Zimam pool, a site's or the panel's, so php.uninstall refuses it
}

type AvailablePHP struct {
	Version     PHPVersion `json:"version"`
	SecurityEnd string     `json:"security_end"` // YYYY-MM-DD, from PHPAllowlist
	Supported   bool       `json:"supported"`    // PHPRelease.SupportedOn(now)
}

// UpdateCounts are the pending updates from updates.json. All three are null
// until the first check.
type UpdateCounts struct {
	Total     *int       `json:"total"`
	Security  *int       `json:"security"`
	CheckedAt *time.Time `json:"checked_at"`
}

// ServicesStatus is the optional services Zimam installs on request.
type ServicesStatus struct {
	MySQL  ServiceStatus `json:"mysql"`
	Valkey ServiceStatus `json:"valkey"`
}

// ServiceStatus is one service: whether its package is installed, its
// version, and whether its unit is active. All three are zero for a service
// that is not installed.
type ServiceStatus struct {
	Installed bool   `json:"installed"`
	Version   string `json:"version"`
	Active    bool   `json:"active"`
}
