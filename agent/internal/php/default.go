package php

import (
	"context"
	"errors"
	"fmt"

	"zimam/agent/contract"
	"zimam/agent/internal/sysexec"
)

const updateAlternatives = "/usr/bin/update-alternatives"

// SetDefault makes v what `php` runs (§8.5): update-alternatives --set for
// php, and for phar and phar.phar where they are registered. It leaves
// pools, vhosts and agent.json alone; bootstrap and php.default store
// default_php themselves.
func SetDefault(ctx context.Context, d sysexec.Deps, v contract.PHPVersion) error {
	if err := v.Validate(); err != nil {
		return fmt.Errorf("PHP %q %v", v, err)
	}
	d.Logf("Making PHP %s the default for the php command", v)
	if _, err := d.Run.Run(ctx, sysexec.Cmd{Path: updateAlternatives, Args: []string{"--set", "php", "/usr/bin/php" + string(v)}}); err != nil {
		return err
	}
	for _, name := range []string{"phar", "phar.phar"} {
		_, err := d.Run.Run(ctx, sysexec.Cmd{Path: updateAlternatives, Args: []string{"--query", name}})
		if errors.As(err, new(*sysexec.ExitError)) {
			continue // not registered
		}
		if err != nil {
			return err
		}
		if _, err := d.Run.Run(ctx, sysexec.Cmd{Path: updateAlternatives, Args: []string{"--set", name, "/usr/bin/" + name + string(v)}}); err != nil {
			return err
		}
	}
	return nil
}
