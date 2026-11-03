// Package logpolicy writes the log rotation rules and the journal's limits
// (design §11.3, §11.4).
package logpolicy

import (
	"bytes"
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
	RetentionDays      contract.RetentionDays
	AuditRetentionDays contract.AuditRetentionDays // the audit log's own (D24)
	SiteCapBytes       contract.SiteCapBytes
}

// Apply renders /etc/zimam/logrotate.conf, Zimam's own rules, one rules file
// per site and the journald drop-in, tests them with logrotate -d, then
// restarts journald.
func Apply(ctx context.Context, d sysexec.Deps, p Policy, sites []contract.SiteName) error {
	if err := p.RetentionDays.Validate(); err != nil {
		return fmt.Errorf("log retention %v", err)
	}
	if err := p.AuditRetentionDays.Validate(); err != nil {
		return fmt.Errorf("audit log retention %v", err)
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
	policy := templates.LogPolicy{RetentionDays: p.RetentionDays, AuditRetentionDays: p.AuditRetentionDays}
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

// ApplySite writes one site's rotation rules and tests them with logrotate
// -d, for a new site (§9.2 step 6); Apply re-renders every site's when the
// policy changes. Rules already as rendered are left alone, so a retry
// changes nothing.
func ApplySite(ctx context.Context, d sysexec.Deps, name contract.SiteName, days contract.RetentionDays) error {
	if err := name.Validate(); err != nil {
		return fmt.Errorf("site %q %v", name, err)
	}
	if err := days.Validate(); err != nil {
		return fmt.Errorf("log retention %v", err)
	}
	content, err := templates.Render("logrotate/site.conf", templates.SiteLogs{Name: name, RetentionDays: days})
	if err != nil {
		return err
	}
	path := d.Paths.SiteLogrotateConf(name)
	if old, err := os.ReadFile(path); err == nil && bytes.Equal(old, content) {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	d.Logf("Writing %s's log rules (%d days), then running logrotate -d", name, days)
	return configtx.Apply(ctx, []configtx.Change{configtx.RootFile(path, content, 0o644)}, func(ctx context.Context) error {
		return configtx.RunTest(ctx, d.Run, sysexec.Cmd{Path: logrotate, Args: []string{"-d", "--state", d.Paths.LogrotateState(), d.Paths.LogrotateConf()}})
	})
}
