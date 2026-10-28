package templates

import "zimam/agent/contract"

// PanelVhost fills nginx/panel.conf (§9.3).
type PanelVhost struct {
	Domain contract.Domain
	HTTPS  bool // the panel's certificate exists
}

// PanelUnits fills the systemd/ templates (§7.1).
type PanelUnits struct{ PanelPHP contract.PHPVersion }

// LogPolicy fills logrotate/zimam-own.conf and journald/50-zimam.conf
// (§11.3).
type LogPolicy struct{ RetentionDays contract.RetentionDays }

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
