// Package systemd installs Zimam's own units: the panel's queue worker and
// scheduler, and the maintenance timer (design §7.1, §11.3).
package systemd

import (
	"context"
	"fmt"
	"regexp"

	"zimam/agent/contract"
	"zimam/agent/internal/configtx"
	"zimam/agent/internal/sysexec"
	"zimam/agent/internal/templates"
)

const (
	systemctl      = "/usr/bin/systemctl"
	systemdAnalyze = "/usr/bin/systemd-analyze"
)

var unitName = regexp.MustCompile(`^zimam-[a-z0-9-]+\.(service|timer)$`)

// Unit is one unit file in /etc/systemd/system.
type Unit struct {
	Name    string // zimam-<name>.service or .timer
	Content []byte
	Enable  bool // enable --now; a service its timer starts stays off
}

// zimamUnits are Zimam's own units in install order, with whether each is
// enabled: the queue worker and the two timers, which start the rest.
var zimamUnits = []struct {
	name   string
	enable bool
}{
	{"zimam-panel-queue.service", true},
	{"zimam-panel-scheduler.service", false},
	{"zimam-panel-scheduler.timer", true},
	{"zimam-maintenance.service", false},
	{"zimam-maintenance.timer", true},
}

// ZimamUnits renders Zimam's own units for the panel's PHP version.
func ZimamUnits(panelPHP contract.PHPVersion) ([]Unit, error) {
	if err := panelPHP.Validate(); err != nil {
		return nil, fmt.Errorf("panel PHP %q %v", panelPHP, err)
	}
	data := templates.PanelUnits{PanelPHP: panelPHP}
	units := make([]Unit, len(zimamUnits))
	for i, u := range zimamUnits {
		content, err := templates.Render("systemd/"+u.name, data)
		if err != nil {
			return nil, err
		}
		units[i] = Unit{Name: u.name, Content: content, Enable: u.enable}
	}
	return units, nil
}

// InstallUnits writes the units through configtx, tested with
// systemd-analyze verify, then reloads systemd and enables and starts each
// unit marked Enable.
func InstallUnits(ctx context.Context, d sysexec.Deps, units []Unit) error {
	var changes []configtx.Change
	verify := []string{"verify"}
	for _, u := range units {
		if !unitName.MatchString(u.Name) {
			return fmt.Errorf("%q isn't a Zimam unit name", u.Name)
		}
		path := d.Paths.SystemdUnit(u.Name)
		changes = append(changes, configtx.RootFile(path, u.Content, 0o644))
		verify = append(verify, path)
	}
	d.Logf("Writing %d units, then running systemd-analyze verify", len(units))
	err := configtx.Apply(ctx, changes, func(ctx context.Context) error {
		return configtx.RunTest(ctx, d.Run, sysexec.Cmd{Path: systemdAnalyze, Args: verify})
	})
	if err != nil {
		return err
	}
	if _, err := d.Run.Run(ctx, sysexec.Cmd{Path: systemctl, Args: []string{"daemon-reload"}}); err != nil {
		return err
	}
	for _, u := range units {
		if !u.Enable {
			continue
		}
		d.Logf("Enabling and starting %s", u.Name)
		if _, err := d.Run.Run(ctx, sysexec.Cmd{Path: systemctl, Args: []string{"enable", "--now", u.Name}}); err != nil {
			return err
		}
	}
	return nil
}
