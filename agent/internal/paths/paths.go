// Package paths names every file and directory the agent uses (design §5.9
// and §9.1). Each path is a method, so tests can move all of them under a
// temporary root.
package paths

import (
	"path/filepath"

	"zimam/agent/contract"
)

// Paths turns every §5.9 path into a method. Root is "" in production and
// t.TempDir() in tests. Arguments must already be validated: the typed ones
// by their contract type, a lock subject by the command that builds it.
type Paths struct{ Root string }

func (p Paths) at(elem ...string) string {
	return filepath.Join(append([]string{p.Root, "/"}, elem...)...)
}

func (p Paths) AgentBinary() string { return p.at("usr/local/sbin/zimam-agent") }

// /etc/zimam

func (p Paths) ConfigDir() string      { return p.at("etc/zimam") }
func (p Paths) AgentConfig() string    { return p.at("etc/zimam/agent.json") }
func (p Paths) InstallerState() string { return p.at("etc/zimam/installer.state") } // D17
func (p Paths) LogrotateConf() string  { return p.at("etc/zimam/logrotate.conf") }
func (p Paths) LogrotateDir() string   { return p.at("etc/zimam/logrotate.d") }
func (p Paths) SiteLogrotateConf(name contract.SiteName) string {
	return p.at("etc/zimam/logrotate.d", "site-"+string(name)+".conf")
}

// /run/zimam

func (p Paths) RunDir() string     { return p.at("run/zimam") }
func (p Paths) Socket() string     { return p.at("run/zimam/agent.sock") }
func (p Paths) LocksDir() string   { return p.at("run/zimam/locks") }
func (p Paths) SubmitLock() string { return p.at("run/zimam/locks/submit.lock") }
func (p Paths) AuditLock() string  { return p.at("run/zimam/locks/audit.lock") }
func (p Paths) ReloadLock() string { return p.at("run/zimam/locks/reload.lock") } // §5.5

// MaintenanceLock keeps two maintenance runs apart, and AptRepoLock two
// setups of the sury repository.
func (p Paths) MaintenanceLock() string { return p.at("run/zimam/locks/maintenance.lock") }
func (p Paths) AptRepoLock() string     { return p.at("run/zimam/locks/apt-repo.lock") }

// SubjectLock is a job subject's lock (§5.7), such as "site:shop" or "server".
func (p Paths) SubjectLock(subject string) string {
	return p.at("run/zimam/locks", subject+".lock")
}

// /var/lib/zimam

func (p Paths) StateDir() string { return p.at("var/lib/zimam") }
func (p Paths) JobsDir() string  { return p.at("var/lib/zimam/jobs") }

// ConfigTxDir holds the record of each config change in flight (configtx).
func (p Paths) ConfigTxDir() string { return p.at("var/lib/zimam/configtx") }
func (p Paths) JobDir(id contract.JobID) string {
	return p.at("var/lib/zimam/jobs", string(id))
}
func (p Paths) JobSpec(id contract.JobID) string  { return filepath.Join(p.JobDir(id), "spec.json") }
func (p Paths) JobState(id contract.JobID) string { return filepath.Join(p.JobDir(id), "state.json") }

// JobCancelLock is held while the job runs apt-get or dpkg, and by
// job.cancel while it stops the job, so a cancel never stops dpkg midway.
func (p Paths) JobCancelLock(id contract.JobID) string {
	return filepath.Join(p.JobDir(id), "cancel.lock")
}
func (p Paths) SitesStateDir() string { return p.at("var/lib/zimam/sites") }

// SiteState is the site's state file (D15).
func (p Paths) SiteState(name contract.SiteName) string {
	return p.at("var/lib/zimam/sites", string(name)+".json")
}
func (p Paths) DownloadsDir() string { return p.at("var/lib/zimam/downloads") }
func (p Paths) CertsDir() string     { return p.at("var/lib/zimam/certs") }

// CertRecord is a certificate's expiry record (§10): name is "panel" or a
// site's name.
func (p Paths) CertRecord(name string) string { return p.at("var/lib/zimam/certs", name+".json") }
func (p Paths) UpdatesCache() string          { return p.at("var/lib/zimam/updates.json") }
func (p Paths) LogrotateState() string        { return p.at("var/lib/zimam/logrotate.state") }

// KeyringRefresh records maintenance's last try at refreshing the sury
// keyring (D23).
func (p Paths) KeyringRefresh() string { return p.at("var/lib/zimam/keyring-refresh.json") }

// What system.status and maintenance read from the system (§7.5): Ubuntu's
// reboot marker and update counter.

func (p Paths) RebootRequired() string { return p.at("run/reboot-required") }
func (p Paths) AptCheck() string       { return p.at("usr/lib/update-notifier/apt-check") }

// What system.metrics reads from /proc (§7.5).

func (p Paths) ProcUptime() string  { return p.at("proc/uptime") }
func (p Paths) ProcLoadavg() string { return p.at("proc/loadavg") }
func (p Paths) ProcStat() string    { return p.at("proc/stat") }
func (p Paths) ProcMeminfo() string { return p.at("proc/meminfo") }
func (p Paths) ProcMounts() string  { return p.at("proc/self/mounts") }
func (p Paths) ProcNetDev() string  { return p.at("proc/net/dev") }

// EtcGroup lists each group's members: site.provision reads whether www-data
// is in a site's group yet (§9.2).
func (p Paths) EtcGroup() string { return p.at("etc/group") }

// /var/log/zimam

func (p Paths) LogDir() string     { return p.at("var/log/zimam") }
func (p Paths) AuditLog() string   { return p.at("var/log/zimam/audit.log") }
func (p Paths) JobLogsDir() string { return p.at("var/log/zimam/jobs") }
func (p Paths) JobLog(id contract.JobID) string {
	return p.at("var/log/zimam/jobs", string(id)+".log")
}
func (p Paths) SitesLogDir() string { return p.at("var/log/zimam/sites") }
func (p Paths) SiteLogDir(name contract.SiteName) string {
	return p.at("var/log/zimam/sites", string(name))
}
func (p Paths) SiteAccessLog(name contract.SiteName) string {
	return filepath.Join(p.SiteLogDir(name), "access.log")
}
func (p Paths) SiteErrorLog(name contract.SiteName) string {
	return filepath.Join(p.SiteLogDir(name), "error.log")
}

// Nginx

func (p Paths) ACMEWebroot() string   { return p.at("var/www/zimam-acme") }
func (p Paths) NginxZimamDir() string { return p.at("etc/nginx/zimam") }
func (p Paths) SiteNginxDir(name contract.SiteName) string {
	return p.at("etc/nginx/zimam", string(name))
}
func (p Paths) SiteSecurityConf(name contract.SiteName) string {
	return filepath.Join(p.SiteNginxDir(name), "security.conf")
}
func (p Paths) SiteHtpasswd(name contract.SiteName) string {
	return filepath.Join(p.SiteNginxDir(name), "htpasswd")
}
func (p Paths) NginxSiteAvailable(name contract.SiteName) string {
	return p.at("etc/nginx/sites-available", "zimam-"+string(name)+".conf")
}
func (p Paths) NginxSiteEnabled(name contract.SiteName) string {
	return p.at("etc/nginx/sites-enabled", "zimam-"+string(name)+".conf")
}

// NginxTLSConf is the TLS snippet every HTTPS vhost includes (§9.3).
func (p Paths) NginxTLSConf() string { return p.at("etc/nginx/zimam/tls.conf") }

// The default server (§9.3), which answers names no site serves, and the
// stock default site's link that bootstrap removes (§4 step 5).
func (p Paths) NginxDefaultAvailable() string {
	return p.at("etc/nginx/sites-available/zimam-default.conf")
}
func (p Paths) NginxDefaultEnabled() string {
	return p.at("etc/nginx/sites-enabled/zimam-default.conf")
}
func (p Paths) NginxDistDefault() string { return p.at("etc/nginx/sites-enabled/default") }

// The panel's vhost (§9.3). "panel" is a reserved site name, so no site's
// files can collide with the panel's.
func (p Paths) NginxPanelAvailable() string {
	return p.at("etc/nginx/sites-available/zimam-panel.conf")
}
func (p Paths) NginxPanelEnabled() string {
	return p.at("etc/nginx/sites-enabled/zimam-panel.conf")
}

// PHP-FPM

func (p Paths) PHPPool(v contract.PHPVersion, name contract.SiteName) string {
	return p.at("etc/php", string(v), "fpm/pool.d", "zimam-"+string(name)+".conf")
}
func (p Paths) PHPSocket(name contract.SiteName, v contract.PHPVersion) string {
	return p.at("run/php", "zimam-"+string(name)+"-"+string(v)+".sock")
}

// PHPFPMBinary is a version's FPM, which exists while the version is
// installed.
func (p Paths) PHPFPMBinary(v contract.PHPVersion) string {
	return p.at("usr/sbin", "php-fpm"+string(v))
}

// Alternative is the link update-alternatives keeps for name, such as php,
// which points at the default version's binary (§8.5).
func (p Paths) Alternative(name string) string { return p.at("etc/alternatives", name) }

func (p Paths) PHPPoolDir(v contract.PHPVersion) string {
	return p.at("etc/php", string(v), "fpm/pool.d")
}

// PanelPool is the panel's pool, on the panel's PHP version (§7.1).
func (p Paths) PanelPool(v contract.PHPVersion) string {
	return filepath.Join(p.PHPPoolDir(v), "zimam-panel.conf")
}

// PHPIni is Zimam's baseline ini file for a version's FPM (§8.3).
func (p Paths) PHPIni(v contract.PHPVersion) string {
	return p.at("etc/php", string(v), "fpm/conf.d/99-zimam.ini")
}

// PHPGlobalConf holds FPM's [global] settings. FPM reads it from pool.d
// like a pool file (§8.3).
func (p Paths) PHPGlobalConf(v contract.PHPVersion) string {
	return filepath.Join(p.PHPPoolDir(v), "00-zimam-global.conf")
}

// PHPWwwPool is the package's stock pool, which php.install deletes (§8.3).
func (p Paths) PHPWwwPool(v contract.PHPVersion) string {
	return filepath.Join(p.PHPPoolDir(v), "www.conf")
}

// systemd: Zimam's own units (§7.1) and the journal's limits (§11.3)

func (p Paths) SystemdUnit(name string) string { return p.at("etc/systemd/system", name) }
func (p Paths) JournaldDropIn() string         { return p.at("etc/systemd/journald.conf.d/50-zimam.conf") }

// OwnLogrotateConf rotates the audit log and the panel's Laravel log (§11.3).
func (p Paths) OwnLogrotateConf() string { return p.at("etc/zimam/logrotate.d/zimam-own.conf") }

// LetsEncryptCert is the certificate certbot keeps in a lineage (§10), such
// as zimam-panel or zimam-shop.
func (p Paths) LetsEncryptCert(certName string) string {
	return p.at("etc/letsencrypt/live", certName, "cert.pem")
}

// APT: the sury repository (§8.2)

func (p Paths) SuryKeyring() string { return p.at("etc/apt/keyrings/zimam-sury.gpg") }
func (p Paths) SurySources() string { return p.at("etc/apt/sources.list.d/zimam-sury.sources") }
func (p Paths) SuryPin() string     { return p.at("etc/apt/preferences.d/zimam-sury.pref") }

// Homes. Root never opens these itself; the helper does, as the owner (D5).

func (p Paths) PanelHome() string        { return p.at("home/zimam") }
func (p Paths) PanelReleasesDir() string { return p.at("home/zimam/panel/releases") }
func (p Paths) PanelCurrent() string     { return p.at("home/zimam/panel/current") }
func (p Paths) PanelSharedDir() string   { return p.at("home/zimam/panel/shared") }
func (p Paths) SiteHome(name contract.SiteName) string {
	return p.at("home", "s_"+string(name))
}
func (p Paths) SiteDir(name contract.SiteName) string {
	return filepath.Join(p.SiteHome(name), "site")
}
func (p Paths) SiteReleasesDir(name contract.SiteName) string {
	return filepath.Join(p.SiteDir(name), "releases")
}
func (p Paths) SiteCurrent(name contract.SiteName) string {
	return filepath.Join(p.SiteDir(name), "current")
}
func (p Paths) SiteTmpDir(name contract.SiteName) string {
	return filepath.Join(p.SiteHome(name), "tmp")
}
func (p Paths) SiteHomeLogsDir(name contract.SiteName) string {
	return filepath.Join(p.SiteHome(name), "logs")
}
func (p Paths) SitePHPErrorLog(name contract.SiteName) string {
	return filepath.Join(p.SiteHomeLogsDir(name), "php-error.log")
}
