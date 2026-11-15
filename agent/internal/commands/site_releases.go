package commands

import (
	"context"

	"zimam/agent/contract"
	"zimam/agent/internal/deploy"
	"zimam/agent/internal/helper"
	"zimam/agent/internal/sysexec"
)

// siteReleases answers site.releases: a routine read, never audited
// (§5.8). It looks at the site's releases through the installed agent
// binary's helper, as logs.read does.
func siteReleases(d sysexec.Deps) Handler {
	return func(ctx context.Context, p contract.Params) (any, error) {
		h := &helper.Client{AgentBinary: d.Paths.AgentBinary()}
		return deploy.Releases(ctx, d, h, helper.LookupAccount, p.(*contract.SiteNameParams).Name)
	}
}
