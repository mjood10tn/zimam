package templates

// Version is the template set's version, recorded per site as
// template_version (design §5.5). Raise it whenever a template's output
// changes, and say how existing sites meet the change.
//
// Version 2 (v0.2.0, R02-D9) adds a laravel site's shared storage logs to
// logrotate/site.conf. Nothing else changed, so a static or PHP site
// renders byte-identical files at 2. Sites provisioned from now on record
// 2; existing ones keep template_version 1, with nothing to re-render.
//
// Version 3 (v0.4.0, R04-D7) adds the per-site worker units and target
// (systemd/zimam-worker.service, systemd/zimam-site.target) and the cron
// file. No existing site has them and no other output changed, so nothing
// is re-rendered on upgrade: existing sites keep their template_version.
const Version = 3
