package contract

import (
	"errors"
	"fmt"
	"slices"
	"time"
)

// ssh.settings writes /etc/ssh/sshd_config.d/10-zimam.conf as a guarded
// change (R06-D1, D4).
func init() {
	Register(Descriptor{
		Name:    "ssh.settings",
		Kind:    KindJob,
		Audited: true,
		Params:  func() Params { return &SSHSettingsParams{} },
		Result:  SSHSettingsResult{},
		Subject: func(Params) string { return "server" },
	})
}

const maxSSHPorts = 4

// RootLogin is PermitRootLogin. "yes" is never offered (R06-D4).
type RootLogin string

var rootLogins = []RootLogin{"no", "prohibit-password"}

func (r RootLogin) Validate() error {
	if slices.Contains(rootLogins, r) {
		return nil
	}
	return errors.New("must be no or prohibit-password")
}

func (RootLogin) JSONSchema() Schema { return Schema{"type": "string", "enum": rootLogins} }

// MaxAuthTries is sshd's MaxAuthTries, 1–10.
type MaxAuthTries int

func (n MaxAuthTries) Validate() error  { return intRange(int(n), 1, 10) }
func (MaxAuthTries) JSONSchema() Schema { return rangeSchema(1, 10) }

// LoginGraceSeconds is sshd's LoginGraceTime, 10–600 seconds.
type LoginGraceSeconds int

func (n LoginGraceSeconds) Validate() error  { return intRange(int(n), 10, 600) }
func (LoginGraceSeconds) JSONSchema() Schema { return rangeSchema(10, 600) }

func intRange(n, lo, hi int) error {
	if n < lo || n > hi {
		return fmt.Errorf("must be %d–%d", lo, hi)
	}
	return nil
}

func rangeSchema(lo, hi int) Schema { return Schema{"type": "integer", "minimum": lo, "maximum": hi} }

type SSHSettingsParams struct {
	PasswordAuth      bool              `json:"password_auth"`
	RootLogin         RootLogin         `json:"root_login"`
	Ports             []TCPPort         `json:"ports"`
	MaxAuthTries      MaxAuthTries      `json:"max_auth_tries"`
	LoginGraceSeconds LoginGraceSeconds `json:"login_grace_seconds"`
}

func (p *SSHSettingsParams) Validate() error {
	f := FieldErrors{}
	f.Check("root_login", p.RootLogin)
	f.Check("max_auth_tries", p.MaxAuthTries)
	f.Check("login_grace_seconds", p.LoginGraceSeconds)
	switch {
	case len(p.Ports) < 1 || len(p.Ports) > maxSSHPorts:
		f["ports"] = fmt.Sprintf("must have 1–%d ports", maxSSHPorts)
	default:
		for i, port := range p.Ports {
			switch {
			case port.Validate() != nil:
				f["ports"] = fmt.Sprintf("port %d must be 1–65535", i+1)
			case port == 80 || port == 443:
				f["ports"] = fmt.Sprintf("port %d is Nginx's", i+1)
			case slices.Contains(p.Ports[:i], port):
				f["ports"] = fmt.Sprintf("port %d is listed twice", i+1)
			}
		}
	}
	return f.Err()
}

// SSHSettingsResult is the guarded change to confirm before Deadline.
type SSHSettingsResult struct {
	Change   ChangeID  `json:"change"`
	Deadline time.Time `json:"deadline"`
}
