// Package logpolicy writes the log rotation rules and the journal's limits
// (design §11.3, §11.4).
package logpolicy

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"zimam/agent/contract"
	"zimam/agent/internal/configtx"
	"zimam/agent/internal/sysexec"
	"zimam/agent/internal/templates"
)

const (
	logrotate = "/usr/sbin/logrotate"
	systemctl = "/usr/bin/systemctl"
)

// Policy is the log policy (§11.4). The rules rotate by RetentionDays;
// zimam-agent maintenance enforces SiteCapBytes.
type Policy struct {
	RetentionDays contract.RetentionDays
	SiteCapBytes  contract.SiteCapBytes
}

// Apply renders /etc/zimam/logrotate.conf, Zimam's own rules, one rules file
// per site and the journald drop-in, tests them with logrotate -d, then
// restarts journald.
func Apply(ctx context.Context, d sysexec.Deps, p Policy, sites []contract.SiteName) error {
	if err := p.RetentionDays.Validate(); err != nil {
		return fmt.Errorf("log retention %v", err)
	}
	if err := p.SiteCapBytes.Validate(); err != nil {
		return fmt.Errorf("site log cap %v", err)
	}
	for _, s := range sites {
		if err := s.Validate(); err != nil {
			return fmt.Errorf("site %q %v", s, err)
		}
	}
	type file struct {
		path, tmpl string
		data       any
	}
	policy := templates.LogPolicy{RetentionDays: p.RetentionDays}
	files := []file{
		{d.Paths.LogrotateConf(), "logrotate/zimam.conf", nil},
		{d.Paths.OwnLogrotateConf(), "logrotate/zimam-own.conf", policy},
		{d.Paths.JournaldDropIn(), "journald/50-zimam.conf", policy},
	}
	for _, s := range sites {
		files = append(files, file{d.Paths.SiteLogrotateConf(s), "logrotate/site.conf", templates.SiteLogs{Name: s, RetentionDays: p.RetentionDays}})
	}
	var changes []configtx.Change
	for _, f := range files {
		content, err := templates.Render(f.tmpl, f.data)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(f.path), 0o755); err != nil {
			return err
		}
		changes = append(changes, configtx.RootFile(f.path, content, 0o644))
	}
	d.Logf("Writing the log rules (%d days) for %d sites, then running logrotate -d", p.RetentionDays, len(sites))
	err := configtx.Apply(ctx, changes, func(ctx context.Context) error {
		return configtx.RunTest(ctx, d.Run, sysexec.Cmd{Path: logrotate, Args: []string{"-d", "--state", d.Paths.LogrotateState(), d.Paths.LogrotateConf()}})
	})
	if err != nil {
		return err
	}
	d.Logf("Restarting journald")
	_, err = d.Run.Run(ctx, sysexec.Cmd{Path: systemctl, Args: []string{"restart", "systemd-journald"}})
	return err
}
