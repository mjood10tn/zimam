// Package sshd writes Zimam's sshd settings as a guarded change (R06-D4):
// one drop-in, tested with sshd -t, with the firewall opened for a new port
// first and closed on the old one only after the owner confirms.
package sshd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"slices"
	"time"

	"zimam/agent/contract"
	"zimam/agent/internal/changes"
	"zimam/agent/internal/configtx"
	"zimam/agent/internal/firewall"
	"zimam/agent/internal/sysexec"
	"zimam/agent/internal/systemd"
	"zimam/agent/internal/templates"
)

// Window is how long the owner has to confirm an SSH change: long enough to
// open a new SSH connection first (R06-D2, OQ2).
const Window = 300 * time.Second

const sshd = "/usr/sbin/sshd"

func init() {
	changes.Register(changes.SSH, changes.Hooks{Revert: revert, Confirm: confirm})
}

// Render is the drop-in for validated settings. PermitRootLogin yes can't
// reach it: Validate refuses it first.
func Render(p contract.SSHSettingsParams) ([]byte, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	return templates.Render("ssh/10-zimam.conf", templates.SSHSettings{
		PasswordAuth: p.PasswordAuth, RootLogin: p.RootLogin, Ports: p.Ports,
		MaxAuthTries: p.MaxAuthTries, LoginGraceSeconds: p.LoginGraceSeconds,
	})
}

// previous is what a revert puts back: the drop-in as it was (or none) and
// UFW's rules before the new ports' rules were added.
type previous struct {
	DropIn   []byte             `json:"drop_in"`
	Missing  bool               `json:"missing,omitempty"`
	Firewall json.RawMessage    `json:"firewall"`
	Ports    []contract.TCPPort `json:"ports"`
}

// Apply writes the settings as a guarded change.
func Apply(ctx context.Context, d sysexec.Deps, p contract.SSHSettingsParams) (contract.ChangeID, time.Time, error) {
	content, err := Render(p)
	if err != nil {
		return "", time.Time{}, err
	}
	ports, err := firewall.SSHPorts(ctx, d)
	if err != nil {
		return "", time.Time{}, err
	}
	prev := previous{Ports: ports}
	prev.DropIn, err = os.ReadFile(d.Paths.SSHDropIn())
	switch {
	case errors.Is(err, fs.ErrNotExist):
		prev.Missing = true
	case err != nil:
		return "", time.Time{}, err
	}
	if prev.Firewall, err = firewall.Snapshot(d); err != nil {
		return "", time.Time{}, err
	}
	b, err := json.Marshal(prev)
	if err != nil {
		return "", time.Time{}, err
	}
	id, deadline, err := changes.Begin(ctx, d, changes.SSH, b, Window)
	if err != nil {
		return "", time.Time{}, err
	}
	portsChanged := !sameSet(ports, p.Ports)
	err = func() error {
		// The new ports' rules first: sshd must never listen where the
		// firewall drops.
		if err := firewall.AddSSHRules(ctx, d, p.Ports); err != nil {
			return err
		}
		if err := configtx.Apply(ctx, []configtx.Change{configtx.RootFile(d.Paths.SSHDropIn(), content, 0o644)}, test(d)); err != nil {
			return err
		}
		return reload(ctx, d, portsChanged)
	}()
	if err != nil {
		return "", time.Time{}, errors.Join(err, changes.Revert(ctx, d, id))
	}
	return id, deadline, nil
}

func test(d sysexec.Deps) func(context.Context) error {
	return func(ctx context.Context) error {
		return configtx.RunTest(ctx, d.Run, sysexec.Cmd{Path: sshd, Args: []string{"-t"}})
	}
}

// reload makes sshd read its settings. With ssh.socket active (Ubuntu's
// default), a port change needs daemon-reload, so sshd-socket-generator
// rewrites the socket's ListenStreams, then a socket restart; open sessions
// survive both (spike). Otherwise ssh.service reloads.
func reload(ctx context.Context, d sysexec.Deps, portsChanged bool) error {
	if portsChanged && systemd.Systemctl(ctx, d, "is-active", "--quiet", "ssh.socket") == nil {
		if err := systemd.Systemctl(ctx, d, "daemon-reload"); err != nil {
			return err
		}
		return systemd.Systemctl(ctx, d, "restart", "ssh.socket")
	}
	return systemd.Systemctl(ctx, d, "reload", "ssh.service")
}

// revert puts the drop-in and the firewall back.
func revert(ctx context.Context, d sysexec.Deps, b []byte) error {
	var prev previous
	if err := json.Unmarshal(b, &prev); err != nil {
		return fmt.Errorf("ssh change record: %w", err)
	}
	c := configtx.RootFile(d.Paths.SSHDropIn(), prev.DropIn, 0o644)
	c.Remove = prev.Missing
	if err := configtx.Apply(ctx, []configtx.Change{c}, test(d)); err != nil {
		return err
	}
	now, err := firewall.SSHPorts(ctx, d)
	if err != nil {
		return err
	}
	if err := reload(ctx, d, !sameSet(now, prev.Ports)); err != nil {
		return err
	}
	return firewall.Restore(ctx, d, prev.Firewall)
}

// confirm closes the firewall on ports sshd left.
func confirm(ctx context.Context, d sysexec.Deps, _ []byte) error {
	return firewall.RemoveStaleSSHRules(ctx, d)
}

func sameSet(a, b []contract.TCPPort) bool {
	a, b = slices.Clone(a), slices.Clone(b)
	slices.Sort(a)
	slices.Sort(b)
	return slices.Equal(a, b)
}
