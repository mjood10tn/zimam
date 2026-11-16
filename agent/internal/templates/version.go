package templates

// Version is the template set's version, recorded per site as
// template_version (design §5.5). Raise it whenever a template's output
// changes, and say how existing sites meet the change.
//
// Version 2 (v0.2.0, R02-D9) adds a laravel site's shared storage logs to
// logrotate/site.conf. Nothing else changed, so a static or PHP site
// renders byte-identical files at 2. Sites provisioned from now on record
// 2; existing ones keep template_version 1, with nothing to re-render.
const Version = 2
