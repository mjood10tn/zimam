package commands

import (
	"context"

	"zimam/agent/contract"
	"zimam/agent/internal/helper"
	"zimam/agent/internal/jobs"
	"zimam/agent/internal/siteprocs"
	"zimam/agent/internal/sites"
	"zimam/agent/internal/sysexec"
)

func init() {
	jobPrechecks["site.php"] = func(d sysexec.Deps) jobs.Precheck {
		return func(_ context.Context, p contract.Params) error {
			return sites.CheckSwitch(d, *p.(*contract.SitePHPParams))
		}
	}
	jobHandlers["site.php"] = sitePHP
}

// sitePHP checks again, since the version may have been uninstalled since
// the job was accepted, then switches the site (§9.5) and moves its scripts
// to the new version.
func sitePHP(ctx context.Context, j *jobs.Context, p contract.Params) (any, error) {
	params := *p.(*contract.SitePHPParams)
	d := j.Deps()
	if err := sites.CheckSwitch(d, params); err != nil {
		return nil, err
	}
	res, err := sites.Switch(ctx, d, params)
	if err != nil {
		return res, err
	}
	// The switch is done and recorded, so what follows can't fail the job:
	// the panel would keep the old version, and sending the switch again is
	// refused as "already runs". The log says what to do instead.
	if err := afterPHPSwitch(ctx, d, j.Helper, helper.LookupAccount, params.Name); err != nil {
		d.Logf("Site %s runs on PHP %s, but moving its workers and cron jobs to it failed: %v. Save the site's workers (or its schedule) to finish it.", params.Name, params.PHPVersion, err)
	}
	return res, nil
}

// afterPHPSwitch points the site's scripts at its new PHP version by
// rewriting only ~/.zimam/site.env.sh, and restarts its workers onto it
// (R04-D15). It never opens a worker or cron script: root never sees the
// owner's command, so it can't rewrite one.
func afterPHPSwitch(ctx context.Context, d sysexec.Deps, h siteprocs.Helper, lookup sites.Lookup, name contract.SiteName) error {
	st, err := sites.Load(d.Paths, name)
	if err != nil {
		return err
	}
	a, err := lookup("s_" + string(name))
	if err != nil {
		return err
	}
	if err := siteprocs.WriteSiteEnv(ctx, h, a, st); err != nil {
		return err
	}
	return siteprocs.RestartWorkers(ctx, d, st)
}
