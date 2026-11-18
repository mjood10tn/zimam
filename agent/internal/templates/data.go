package templates

import (
	"crypto/sha256"
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
	Laravel       bool // also rotates site/shared/storage/logs (R02-D8)
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

// LaravelEnv fills site/laravel.env, a laravel site's first shared/.env
// (R02-D8). The helper writes it as the site user; root never reads it.
// AppKey is a secret: it must never reach argv, a log or an error.
type LaravelEnv struct {
	Domain contract.Domain
	AppKey string // "base64:" and 32 random bytes in standard base64
}

// SitePlaceholder fills site/index.html and site/index.php, the page a new
// site serves until its first deploy (§9.2).
type SitePlaceholder struct {
	Name contract.SiteName
}

// MySQLConf fills mysql/zz-zimam.cnf. The buffer pool is a whole number of
// MiB, which the agent works out from the server's memory.
type MySQLConf struct{ BufferPoolMiB int }

// ValkeyConf fills valkey/valkey.conf. The memory limit is a whole number of
// MiB, which the agent works out from the server's memory.
type ValkeyConf struct{ MaxMemoryMiB int }

// ValkeyACL fills valkey/users.acl: the admin user and one user per site
// that has a cache user. Passwords never reach the file, only the SHA-256 of
// each, which Valkey accepts as "#<hex>".
type ValkeyACL struct {
	AdminHash [sha256.Size]byte
	Sites     []ValkeySite // sorted by name
}

// ValkeySite is one site's Valkey user, s_<name>.
type ValkeySite struct {
	Name contract.SiteName
	Hash [sha256.Size]byte
}

// SiteEnv fills site/site.env.sh (R04-D15), which every worker and cron
// script of a site sources. The helper writes it as the site user.
type SiteEnv struct {
	PHPVersion contract.PHPVersion // empty for a static site: no php directory
}

// WorkerScript fills site/worker.sh (R04-D1). A laravel-queue worker sets Argv,
// the whole artisan command, whose every word the template single-quotes; a
// custom worker sets Command, the owner's line, which goes in verbatim. Both
// are written as the site user and never read by root.
type WorkerScript struct {
	Argv    []string
	Command contract.OwnerCommand
}

// CronScript fills site/cron.sh (R04-D1): one cron job, or the Laravel
// scheduler with ID "scheduler" and its artisan line as Command.
type CronScript struct {
	ID      contract.WorkerID
	Command contract.OwnerCommand
}
