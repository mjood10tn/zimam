package contract

import (
	"errors"
	"regexp"
	"slices"
	"time"
)

// The OS update commands (v0.6.0 plan, R06-D12..D16): automatic security
// updates, installing pending updates now, and reboots.
func init() {
	Register(Descriptor{
		Name:    "updates.auto",
		Kind:    KindJob,
		Audited: true,
		Params:  func() Params { return &UpdatesAutoParams{} },
		Result:  AutoUpdates{},
		Subject: func(Params) string { return "server" },
	})
	Register(Descriptor{
		Name:    "updates.install",
		Kind:    KindJob,
		Audited: true,
		Params:  func() Params { return &UpdatesInstallParams{} },
		Result:  UpdatesInstallResult{},
		Subject: func(Params) string { return "server" },
	})
	Register(Descriptor{
		Name:    "system.reboot",
		Kind:    KindJob,
		Audited: true,
		Params:  func() Params { return &SystemRebootParams{} },
		Result:  SystemRebootResult{},
		Subject: func(Params) string { return "server" },
	})
	Register(Descriptor{
		Name:    "system.reboot.cancel",
		Kind:    KindJob,
		Audited: true,
		Params:  func() Params { return &SystemRebootCancelParams{} },
		Result:  SystemRebootCancelResult{},
		Subject: func(Params) string { return "server" },
	})
}

var clockTimeRe = regexp.MustCompile(`^([01][0-9]|2[0-3]):[0-5][0-9]$`)

// ClockTime is HH:MM in the server's time zone.
type ClockTime string

func (c ClockTime) Validate() error {
	if !clockTimeRe.MatchString(string(c)) {
		return errors.New("must be HH:MM, 00:00 to 23:59")
	}
	return nil
}

func (ClockTime) JSONSchema() Schema {
	return Schema{"type": "string", "pattern": clockTimeRe.String()}
}

// UpdatesAutoParams turns Ubuntu's automatic security updates on or off
// (R06-D12) and, with them, the automatic reboot at RebootTime (R06-D16).
type UpdatesAutoParams struct {
	Enabled    bool      `json:"enabled"`
	AutoReboot bool      `json:"auto_reboot"`
	RebootTime ClockTime `json:"reboot_time,omitempty"`
}

func (p *UpdatesAutoParams) Validate() error {
	f := FieldErrors{}
	switch {
	case p.AutoReboot && p.RebootTime == "":
		f["reboot_time"] = "is required with auto_reboot"
	case p.RebootTime != "":
		f.Check("reboot_time", p.RebootTime)
	}
	return f.Err()
}

// AutoUpdates is what apt's configuration says: updates.auto's result and
// system.status's auto_updates. RebootTime is set only with AutoReboot.
type AutoUpdates struct {
	Enabled    bool      `json:"enabled"`
	AutoReboot bool      `json:"auto_reboot"`
	RebootTime ClockTime `json:"reboot_time,omitempty"`
}

// UpdatesScope is what updates.install upgrades (R06-D13): Ubuntu's security
// origins only, or every package apt-get upgrade would.
type UpdatesScope string

var updatesScopes = []UpdatesScope{"security", "all"}

func (s UpdatesScope) Validate() error {
	if slices.Contains(updatesScopes, s) {
		return nil
	}
	return errors.New("must be security or all")
}

func (UpdatesScope) JSONSchema() Schema { return Schema{"type": "string", "enum": updatesScopes} }

type UpdatesInstallParams struct {
	Scope UpdatesScope `json:"scope"`
}

func (p *UpdatesInstallParams) Validate() error {
	f := FieldErrors{}
	f.Check("scope", p.Scope)
	return f.Err()
}

// UpgradedPackage is one package whose version changed; From is empty for
// one the upgrade newly installed.
type UpgradedPackage struct {
	Package string `json:"package"`
	From    string `json:"from"`
	To      string `json:"to"`
}

// UpdatesInstallResult is what changed: the packages, the services
// needrestart restarted, whether a reboot is needed, and the security
// updates still pending once the counts were refreshed (null when the count
// failed).
type UpdatesInstallResult struct {
	Scope                UpdatesScope      `json:"scope"`
	Upgraded             []UpgradedPackage `json:"upgraded"`
	Restarted            []string          `json:"restarted"`
	RebootRequired       bool              `json:"reboot_required"`
	SecurityPendingAfter *int              `json:"security_pending_after"`
}

// RebootAt is "now" or HH:MM in the server's time zone, which shutdown
// takes as the next such time, so always within 24 hours (R06-D14).
type RebootAt string

func (a RebootAt) Validate() error {
	if a == "now" || clockTimeRe.MatchString(string(a)) {
		return nil
	}
	return errors.New(`must be "now" or HH:MM`)
}

func (RebootAt) JSONSchema() Schema {
	return Schema{"type": "string", "pattern": `^(now|([01][0-9]|2[0-3]):[0-5][0-9])$`}
}

type SystemRebootParams struct {
	At RebootAt `json:"at"`
}

func (p *SystemRebootParams) Validate() error {
	f := FieldErrors{}
	f.Check("at", p.At)
	return f.Err()
}

// SystemRebootResult says when the server reboots.
type SystemRebootResult struct {
	At          RebootAt  `json:"at"`
	ScheduledAt time.Time `json:"scheduled_at"`
}

type SystemRebootCancelParams struct{}

func (*SystemRebootCancelParams) Validate() error { return nil }

// SystemRebootCancelResult says whether a reboot was scheduled.
type SystemRebootCancelResult struct {
	Cancelled bool `json:"cancelled"`
}
