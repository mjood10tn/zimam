package templates

import (
	"net/netip"

	"zimam/agent/contract"
)

// PanelVhost fills nginx/panel.conf (§9.3).
type PanelVhost struct {
	Domain contract.Domain
	HTTPS  bool // the panel's certificate exists
}

// PanelUnits fills the systemd/ templates (§7.1).
type PanelUnits struct{ PanelPHP contract.PHPVersion }

// LogPolicy fills logrotate/zimam-own.conf and journald/50-zimam.conf
// (§11.3).
type LogPolicy struct {
	RetentionDays      contract.RetentionDays
	AuditRetentionDays contract.AuditRetentionDays // the audit log's own (D24)
}

// SiteLogs fills logrotate/site.conf (§11.3).
type SiteLogs struct {
	Name          contract.SiteName
	RetentionDays contract.RetentionDays
}

// SuryRepo fills apt/zimam-sury.sources and apt/zimam-sury.pref (§8.2).
type SuryRepo struct {
	URL  string // php_repo_url, the source's URIs
	Host string // its host, which the pin names as the origin
}

// SiteVhost fills nginx/site.conf (§9.3).
type SiteVhost struct {
	Name       contract.SiteName
	Domain     contract.Domain
	PHPVersion contract.PHPVersion // empty for a static site
	HTTPS      bool                // the site's certificate exists
	ForceHTTPS bool                // the HTTP server redirects (§9.6); only with HTTPS
}

// SiteSecurity fills nginx/security.conf (§9.6). IP entries are the parsed
// prefixes, never the raw input (D21).
type SiteSecurity struct {
	Name                  contract.SiteName // names the htpasswd file
	HeadersPreset         bool
	HSTSMaxAge            contract.HSTSMaxAge // 0 is off
	HSTSIncludeSubdomains bool
	IPDeny                []netip.Prefix
	IPAllow               []netip.Prefix // when not empty, everyone else is denied
	PasswordProtection    bool
}

// Htpasswd fills nginx/htpasswd (§9.6): one "username:bcrypt hash" line per
// user.
type Htpasswd struct {
	Users contract.BasicAuthUsers
}

// SitePool fills php/site-pool.conf (§9.4).
type SitePool struct {
	Name                 contract.SiteName
	PHPVersion           contract.PHPVersion
	BlockRiskyFunctions  bool
	RestrictToSiteFolder bool
}

// SitePlaceholder fills site/index.html and site/index.php, the page a new
// site serves until its first deploy (§9.2).
type SitePlaceholder struct {
	Name contract.SiteName
}
