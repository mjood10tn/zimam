package cloudflare

import (
	"bytes"
	"context"
	"os"

	"zimam/agent/contract"
	"zimam/agent/internal/configtx"
	"zimam/agent/internal/firewall"
	"zimam/agent/internal/nginx"
	"zimam/agent/internal/sysexec"
	"zimam/agent/internal/templates"
)

// Mode turns Cloudflare mode on or off. The realip file is a plain tested
// change; turning the origin restriction on or off is a guarded firewall
// change the owner confirms (R06-D1, D9), so the result names it.
func Mode(ctx context.Context, d sysexec.Deps, p contract.CloudflareModeParams) (contract.CloudflareModeResult, error) {
	res := contract.CloudflareModeResult{Enabled: p.Enabled, RestrictOrigin: p.RestrictOrigin}
	r, _, err := Current(d.Paths)
	if err != nil {
		return res, err
	}
	was, err := firewall.WebRestricted(d)
	if err != nil {
		return res, err
	}
	if err := writeRealIP(ctx, d, p.Enabled, r); err != nil {
		return res, err
	}
	if p.RestrictOrigin == was {
		return res, nil
	}
	from := contract.IPList{}
	if p.RestrictOrigin {
		from = r.All()
	}
	id, deadline, err := firewall.Guarded(ctx, d, func() error { return firewall.WebSources(ctx, d, from) })
	if err != nil {
		return res, err
	}
	res.Change, res.Deadline = &id, &deadline
	return res, nil
}

// writeRealIP writes zimam-realip.conf, or removes it when off, tested with
// nginx -t, and reloads Nginx. A file already as wanted changes nothing.
func writeRealIP(ctx context.Context, d sysexec.Deps, on bool, r Ranges) error {
	path := d.Paths.NginxRealIPConf()
	old, err := os.ReadFile(path)
	exists := err == nil
	if !on {
		if !exists {
			return nil
		}
		return nginx.ApplySite(ctx, d, []configtx.Change{{Path: path, Remove: true}}, nginx.Link{})
	}
	conf, err := templates.Render("nginx/zimam-realip.conf", templates.CloudflareRealIP{Ranges: r.prefixes()})
	if err != nil {
		return err
	}
	if exists && bytes.Equal(old, conf) {
		return nil
	}
	return nginx.ApplySite(ctx, d, []configtx.Change{configtx.RootFile(path, conf, 0o644)}, nginx.Link{})
}
