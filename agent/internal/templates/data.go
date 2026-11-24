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
// scheduler with ID "scheduler" and its artisan line as Command. A job takes
// flock -n on its own lock; the scheduler takes none (ruling R8, against
// R04-D1's text): Laravel expects schedule:run every minute and handles
// overlap per task (withoutOverlapping), so a lock would skip every task due
// while one long task runs.
type CronScript struct {
	ID      contract.WorkerID
	Command contract.OwnerCommand
}

// WorkerUnit fills systemd/zimam-worker.service, one worker's template unit
// (R04-D2). It holds only validated fields: the owner's command lives in the
// script the unit names.
type WorkerUnit struct {
	Site           contract.SiteName
	ID             contract.WorkerID
	MemoryMB       contract.WorkerMemoryMB
	TimeoutStopSec int // the worker's timeout plus 30
}

// BackupUnit fills systemd/zimam-backup.service and .timer, one plan's units
// (R05-D7).
type BackupUnit struct {
	Plan     contract.PlanID
	Schedule contract.BackupSchedule
}

// SiteTarget fills systemd/zimam-site.target, which groups a site's workers.
type SiteTarget struct{ Site contract.SiteName }

// SiteCron fills cron/site.cron, a site's /etc/cron.d/zimam-<site> (R04-D5):
// a line for the Laravel scheduler when it is on, then one per enabled job.
// Every line runs as s_<Site>, and the owner's command is in the job's script,
// never in this file.
type SiteCron struct {
	Site      contract.SiteName
	Scheduler bool
	Jobs      []CronLine
}

// CronLine is one enabled job. Schedule must be in normal form, as
// contract.ParseCronSchedule returns it.
type CronLine struct {
	ID       contract.WorkerID
	Schedule contract.CronSchedule
}

// NodeRepo fills apt/zimam-nodesource.sources (R04-D6).
type NodeRepo struct {
	Major contract.NodeMajor // the one NodeSource major APT follows
}

// RcloneConf fills backup/rclone.conf (R05-D2): every backup destination,
// sorted by id.
type RcloneConf struct{ Remotes []RcloneRemote }

// RcloneRemote is one destination: a raw s3 or sftp remote "<ID>-raw" and a
// crypt remote "<ID>" over Target on it. Password and Password2 are already
// in rclone's obscured form, which is base64url.
type RcloneRemote struct {
	ID                contract.DestinationID
	S3                *contract.S3Settings   // or SFTP
	SFTP              *contract.SFTPSettings // its fingerprint isn't rendered: the pin is the known_hosts file
	KeyFile           string                 // SFTP
	KnownHostsFile    string                 // SFTP
	HostKeyAlgorithms string                 // SFTP, from rclone.HostKeyAlgorithms; empty until a key is pinned
	Target            string                 // bucket[/prefix] or the SFTP path
	Password          string
	Password2         string
}

// SSHSettings is ssh/10-zimam.conf (R06-D4).
type SSHSettings struct {
	PasswordAuth      bool
	RootLogin         contract.RootLogin
	Ports             []contract.TCPPort
	MaxAuthTries      contract.MaxAuthTries
	LoginGraceSeconds contract.LoginGraceSeconds
}

// Fail2banJails is fail2ban/zimam.local (R06-D5): every jail's settings, and
// the addresses no jail bans, parsed.
type Fail2banJails struct {
	Ignore []netip.Prefix
	Jails  []contract.Fail2banJail
}

// CloudflareRealIP is nginx/zimam-realip.conf (R06-D7): Cloudflare's ranges,
// parsed.
type CloudflareRealIP struct {
	Ranges []netip.Prefix
}

// AutoUpgrades is apt/21zimam-auto-upgrades (R06-D12).
type AutoUpgrades struct{ Enabled bool }

// UnattendedReboot is apt/52zimam-unattended-reboot (R06-D16). RebootTime is
// rendered only with AutoReboot.
type UnattendedReboot struct {
	AutoReboot bool
	RebootTime contract.ClockTime
}
