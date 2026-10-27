// Package config reads and writes /etc/zimam/agent.json (design §5.9).
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/mail"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"unicode"

	"zimam/agent/contract"
	"zimam/agent/internal/paths"
)

// Defaults from §5.9.
const (
	DefaultACMEDirectory    = "https://acme-v02.api.letsencrypt.org/directory"
	DefaultPHPRepoURL       = "https://packages.sury.org/php/"
	DefaultLogRetentionDays = 14
	DefaultSiteLogCapBytes  = 5 * contract.GiB
)

// Agent is /etc/zimam/agent.json (§5.9).
type Agent struct {
	PanelDomain      contract.Domain        `json:"panel_domain"`
	ACMEDirectory    string                 `json:"acme_directory"`
	ACMECABundle     string                 `json:"acme_ca_bundle,omitempty"`
	ACMEEmail        string                 `json:"acme_email,omitempty"`
	PHPRepoURL       string                 `json:"php_repo_url"`
	DefaultPHP       contract.PHPVersion    `json:"default_php"`
	PanelPHP         contract.PHPVersion    `json:"panel_php"`
	LogRetentionDays contract.RetentionDays `json:"log_retention_days"`
	SiteLogCapBytes  contract.SiteCapBytes  `json:"site_log_cap_bytes"`
}

// New returns the config bootstrap writes, with the §5.9 defaults. Bootstrap
// then sets the ACME fields from the installer's flags.
func New(panelDomain contract.Domain, defaultPHP, panelPHP contract.PHPVersion) Agent {
	return Agent{
		PanelDomain:      panelDomain,
		ACMEDirectory:    DefaultACMEDirectory,
		PHPRepoURL:       DefaultPHPRepoURL,
		DefaultPHP:       defaultPHP,
		PanelPHP:         panelPHP,
		LogRetentionDays: DefaultLogRetentionDays,
		SiteLogCapBytes:  contract.SiteCapBytes(DefaultSiteLogCapBytes),
	}
}

// Validate checks every field. Values end up in APT sources, certbot
// arguments and templates, so URLs, paths and the email carry no whitespace.
func (a Agent) Validate() error {
	f := contract.FieldErrors{}
	f.Check("panel_domain", a.PanelDomain)
	f.Check("acme_directory", httpsURL(a.ACMEDirectory))
	f.Check("php_repo_url", httpsURL(a.PHPRepoURL))
	if a.ACMECABundle != "" {
		f.Check("acme_ca_bundle", absPath(a.ACMECABundle))
	}
	if a.ACMEEmail != "" {
		f.Check("acme_email", email(a.ACMEEmail))
	}
	f.Check("default_php", a.DefaultPHP)
	f.Check("panel_php", a.PanelPHP)
	f.Check("log_retention_days", a.LogRetentionDays)
	f.Check("site_log_cap_bytes", a.SiteLogCapBytes)
	return f.Err()
}

// Load reads agent.json strictly, then validates it.
func Load(p paths.Paths) (Agent, error) {
	data, err := os.ReadFile(p.AgentConfig())
	if err != nil {
		return Agent{}, err
	}
	var a Agent
	if err := contract.DecodeStrict(data, &a); err != nil {
		return Agent{}, fmt.Errorf("%s: %w", p.AgentConfig(), err)
	}
	if err := a.Validate(); err != nil {
		return Agent{}, fmt.Errorf("%s: %w", p.AgentConfig(), err)
	}
	return a, nil
}

// Save validates a and writes it atomically: a temporary file in the same
// directory, mode 0600, synced, then renamed over agent.json.
func Save(p paths.Paths, a Agent) error {
	if err := a.Validate(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(a, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	dir := p.ConfigDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".agent.json.*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // fails harmlessly once renamed
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), p.AgentConfig()); err != nil {
		return err
	}
	return syncDir(dir)
}

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

func hasSpaceOrControl(s string) bool {
	return strings.IndexFunc(s, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0
}

type httpsURL string

func (u httpsURL) Validate() error {
	s := string(u)
	if hasSpaceOrControl(s) {
		return errors.New("must not contain whitespace or control characters")
	}
	parsed, err := url.Parse(s)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" {
		return errors.New("must be an https URL with a host and no credentials")
	}
	return nil
}

type absPath string

func (p absPath) Validate() error {
	s := string(p)
	if hasSpaceOrControl(s) || !filepath.IsAbs(s) || filepath.Clean(s) != s {
		return errors.New("must be a clean absolute path without whitespace")
	}
	return nil
}

type email string

func (e email) Validate() error {
	s := string(e)
	addr, err := mail.ParseAddress(s)
	if err != nil || addr.Address != s || hasSpaceOrControl(s) {
		return errors.New("must be a plain email address")
	}
	return nil
}
