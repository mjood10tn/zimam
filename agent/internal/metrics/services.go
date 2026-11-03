package metrics

import (
	"context"
	"fmt"
	"strings"

	"zimam/agent/contract"
	"zimam/agent/internal/php"
	"zimam/agent/internal/sysexec"
)

const systemctl = "/usr/bin/systemctl"

// fixedUnits are monitored on every server (§7.5), after nginx and the FPM
// services. Both SSH units are asked about in the same call; whichever is
// enabled is reported.
var fixedUnits = []string{
	"zimam-agent.socket",
	"zimam-panel-queue.service",
	"zimam-panel-scheduler.timer",
	"zimam-maintenance.timer",
	"certbot.timer",
	"ssh.socket",
	"ssh.service",
}

// Services reports the state of every monitored unit (§7.5) from one
// systemctl show call: nginx, php<v>-fpm for each version with a Zimam
// pool (a version without one is stopped on purpose, §8.3), Zimam's own
// units, certbot's timer, and ssh.socket or ssh.service, whichever is
// enabled, preferring the socket that Ubuntu 26.04 starts sshd from.
func Services(ctx context.Context, d sysexec.Deps) ([]contract.ServiceState, error) {
	units := []string{"nginx.service"}
	for _, r := range contract.PHPAllowlist {
		pools, err := php.HasPools(d, r.Version)
		if err != nil {
			return nil, err
		}
		if pools {
			units = append(units, "php"+string(r.Version)+"-fpm.service")
		}
	}
	units = append(units, fixedUnits...)
	res, err := d.Run.Run(ctx, sysexec.Cmd{Path: systemctl, Args: append([]string{"show", "-p", "Id,ActiveState,UnitFileState", "--"}, units...)})
	if err != nil {
		return nil, err
	}
	states, err := parseShow(string(res.Stdout), units)
	if err != nil {
		return nil, err
	}
	out := states[:len(states)-2]
	socket, service := states[len(states)-2], states[len(states)-1]
	switch {
	case enabled(socket.UnitFileState):
		out = append(out, socket)
	case enabled(service.UnitFileState):
		out = append(out, service)
	}
	return out, nil
}

func enabled(state string) bool { return state == "enabled" || state == "enabled-runtime" }

// parseShow reads systemctl show's blocks, one per unit in the order asked
// and separated by blank lines, whatever order the properties come in. Each
// block must name the unit asked for, so one unit's state is never pinned on
// another.
func parseShow(out string, units []string) ([]contract.ServiceState, error) {
	var blocks []map[string]string
	cur := map[string]string{}
	for line := range strings.Lines(out + "\n") {
		line = strings.TrimRight(line, "\n")
		if line == "" {
			if len(cur) > 0 {
				blocks = append(blocks, cur)
				cur = map[string]string{}
			}
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			return nil, fmt.Errorf("systemctl show printed %q", line)
		}
		cur[k] = v
	}
	if len(blocks) != len(units) {
		return nil, fmt.Errorf("systemctl show answered for %d units, want %d", len(blocks), len(units))
	}
	states := make([]contract.ServiceState, len(units))
	for i, b := range blocks {
		active, okActive := b["ActiveState"]
		file, okFile := b["UnitFileState"]
		switch {
		case b["Id"] != units[i]:
			return nil, fmt.Errorf("systemctl show answered %q where %s was asked for", b["Id"], units[i])
		case !okActive || !okFile:
			return nil, fmt.Errorf("systemctl show left out a property of %s", units[i])
		}
		states[i] = contract.ServiceState{Unit: units[i], ActiveState: active, UnitFileState: file}
	}
	return states, nil
}
