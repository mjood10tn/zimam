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
	Node           *NodeStatus    `json:"node,omitempty"` // R04-D6; absent on a v0.3 agent, and when dpkg could not be read
	Timezone       string         `json:"timezone"`       // IANA name of the server's zone, UTC when unreadable (ruling R7); absent on an older agent
	// R06-D12, D16: apt's automatic updates, absent when apt-config could not
	// be read; and a scheduled reboot (R06-D14), absent when none is.
	AutoUpdates     *AutoUpdates `json:"auto_updates,omitempty"`
	RebootScheduled *time.Time   `json:"reboot_scheduled,omitempty"`
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

// ServicesStatus is the optional services Zimam installs on request. A
// service the agent could not read is left out (unknown), which is not the
// same as one that is not installed.
type ServicesStatus struct {
	MySQL  *ServiceStatus `json:"mysql,omitempty"`
	Valkey *ServiceStatus `json:"valkey,omitempty"`
}

// ServiceStatus is one service: whether its package is installed, its
// version, and whether its unit is active. All three are zero for a service
// that is not installed.
type ServiceStatus struct {
	Installed bool   `json:"installed"`
	Version   string `json:"version"`
	Active    bool   `json:"active"`
}

// NodeStatus is what system.status reports of Node.js (R04-D6), read live:
// Node is not in services.json. Major is the installed package's major, which
// is off the allowlist only for a nodejs that Zimam did not install. Available
// carries the allowlist and its end dates so the panel never hard-codes them.
type NodeStatus struct {
	Installed bool            `json:"installed"`
	Major     string          `json:"major,omitempty"`
	Version   string          `json:"version,omitempty"`
	Available []AvailableNode `json:"available"`
}

type AvailableNode struct {
	Major       NodeMajor `json:"major"`
	EndOfLife   string    `json:"end_of_life"` // YYYY-MM-DD, from NodeAllowlist
	Supported   bool      `json:"supported"`   // NodeRelease.SupportedOn(now)
	Recommended bool      `json:"recommended"` // DefaultNodeMajor
}
