package jobs

import (
	"context"
	"fmt"
	"strings"

	"zimam/agent/internal/sysexec"
)

// Systemd is what jobs need from systemd (§5.7).
type Systemd interface {
	StartTransient(ctx context.Context, unit string, props []string, argv []string) error // systemd-run --no-block
	Stop(ctx context.Context, unit string) error
	Show(ctx context.Context, unit string) (UnitState, error) // LoadState, ActiveState, Result
	ResetFailed(ctx context.Context, unit string) error
}

// UnitState is what systemctl show reports about a job's unit. A unit
// systemd doesn't know has LoadState "not-found".
type UnitState struct {
	LoadState, ActiveState, Result string
}

const (
	systemdRun = "/usr/bin/systemd-run"
	systemctl  = "/usr/bin/systemctl"
)

// Systemctl is the real Systemd: systemd-run and systemctl, through Run.
type Systemctl struct{ Run sysexec.Runner }

// StartTransient runs argv in a new transient unit and returns without
// waiting for it. The unit isn't --collect'ed, so a failed one stays
// visible until Reconcile resets it (§5.7).
func (s Systemctl) StartTransient(ctx context.Context, unit string, props, argv []string) error {
	args := []string{"--unit=" + unit}
	for _, p := range props {
		args = append(args, "--property="+p)
	}
	args = append(append(args, "--no-block", "--"), argv...)
	_, err := s.Run.Run(ctx, sysexec.Cmd{Path: systemdRun, Args: args})
	return err
}

func (s Systemctl) Stop(ctx context.Context, unit string) error {
	_, err := s.Run.Run(ctx, sysexec.Cmd{Path: systemctl, Args: []string{"stop", unit}})
	return err
}

func (s Systemctl) Show(ctx context.Context, unit string) (UnitState, error) {
	res, err := s.Run.Run(ctx, sysexec.Cmd{Path: systemctl, Args: []string{"show", "--property=LoadState,ActiveState,Result", unit}})
	if err != nil {
		return UnitState{}, err
	}
	var u UnitState
	for line := range strings.Lines(string(res.Stdout)) {
		key, value, _ := strings.Cut(strings.TrimSpace(line), "=")
		switch key {
		case "LoadState":
			u.LoadState = value
		case "ActiveState":
			u.ActiveState = value
		case "Result":
			u.Result = value
		}
	}
	if u.LoadState == "" {
		return UnitState{}, fmt.Errorf("systemctl show %s printed no LoadState: %q", unit, res.Stdout)
	}
	return u, nil
}

func (s Systemctl) ResetFailed(ctx context.Context, unit string) error {
	_, err := s.Run.Run(ctx, sysexec.Cmd{Path: systemctl, Args: []string{"reset-failed", unit}})
	return err
}
