package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"zimam/agent/contract"
	"zimam/agent/internal/paths"
)

func testAgent() Agent {
	return New("panel.example.com", "8.5", "8.4")
}

func TestConfigDefaults(t *testing.T) {
	a := testAgent()
	if a.LogRetentionDays != 14 {
		t.Errorf("LogRetentionDays = %d, want 14", a.LogRetentionDays)
	}
	if a.SiteLogCapBytes != contract.SiteCapBytes(5*contract.GiB) {
		t.Errorf("SiteLogCapBytes = %d, want 5 GiB", a.SiteLogCapBytes)
	}
	if a.ACMEDirectory != "https://acme-v02.api.letsencrypt.org/directory" {
		t.Errorf("ACMEDirectory = %q, want Let's Encrypt production", a.ACMEDirectory)
	}
	if a.PHPRepoURL != "https://packages.sury.org/php/" {
		t.Errorf("PHPRepoURL = %q, want sury", a.PHPRepoURL)
	}
	if a.PanelDomain != "panel.example.com" || a.DefaultPHP != "8.5" || a.PanelPHP != "8.4" {
		t.Errorf("New did not keep its arguments: %+v", a)
	}
	if a.ACMECABundle != "" || a.ACMEEmail != "" {
		t.Errorf("optional fields are set: %+v", a)
	}
	if err := a.Validate(); err != nil {
		t.Errorf("defaults do not validate: %v", err)
	}
}

func TestConfigSaveLoad(t *testing.T) {
	p := paths.Paths{Root: t.TempDir()}
	a := testAgent()
	a.ACMEEmail = "admin@example.com"
	a.ACMECABundle = "/etc/zimam/pebble.pem"
	if err := Save(p, a); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got != a {
		t.Errorf("Load = %+v, want %+v", got, a)
	}
	data, _ := os.ReadFile(p.AgentConfig())
	if !strings.Contains(string(data), `"panel_domain": "panel.example.com"`) || !strings.HasSuffix(string(data), "}\n") {
		t.Errorf("unexpected file:\n%s", data)
	}
}

func TestConfigSaveMode0600(t *testing.T) {
	p := paths.Paths{Root: t.TempDir()}
	if err := os.MkdirAll(p.ConfigDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.AgentConfig(), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Save(p, testAgent()); err != nil {
		t.Fatalf("Save: %v", err)
	}
	fi, err := os.Stat(p.AgentConfig())
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("mode = %v, want 0600", fi.Mode().Perm())
	}
	entries, _ := os.ReadDir(p.ConfigDir())
	if len(entries) != 1 {
		t.Errorf("config dir holds %d entries, want only agent.json (no temp files left)", len(entries))
	}
}

func TestConfigSaveCreatesDir(t *testing.T) {
	p := paths.Paths{Root: t.TempDir()}
	if err := Save(p, testAgent()); err != nil {
		t.Fatalf("Save into a missing /etc/zimam: %v", err)
	}
	fi, err := os.Stat(p.ConfigDir())
	if err != nil || fi.Mode().Perm() != 0o755 {
		t.Errorf("config dir: %v, %v; want 0755", fi, err)
	}
}

func TestConfigSaveRejectsInvalid(t *testing.T) {
	p := paths.Paths{Root: t.TempDir()}
	a := testAgent()
	a.DefaultPHP = "8.1"
	if err := Save(p, a); err == nil {
		t.Fatal("Save accepted an invalid config")
	}
	if _, err := os.Stat(p.AgentConfig()); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("an invalid config was written: %v", err)
	}
}

func writeConfig(t *testing.T, p paths.Paths, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p.AgentConfig()), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.AgentConfig(), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

const validJSON = `{"panel_domain":"panel.example.com","acme_directory":"https://acme-v02.api.letsencrypt.org/directory","php_repo_url":"https://packages.sury.org/php/","default_php":"8.5","panel_php":"8.4","log_retention_days":14,"site_log_cap_bytes":5368709120}`

func TestConfigRejectsUnknownField(t *testing.T) {
	p := paths.Paths{Root: t.TempDir()}
	writeConfig(t, p, validJSON)
	if _, err := Load(p); err != nil {
		t.Fatalf("valid config: %v", err)
	}
	writeConfig(t, p, strings.Replace(validJSON, `{`, `{"extra":1,`, 1))
	if _, err := Load(p); err == nil {
		t.Error("Load accepted an unknown field")
	}
}

func TestConfigLoadRejects(t *testing.T) {
	for name, body := range map[string]string{
		"missing panel_domain":   strings.Replace(validJSON, `"panel_domain":"panel.example.com",`, ``, 1),
		"bad domain":             strings.Replace(validJSON, `panel.example.com`, `panel example.com`, 1),
		"http ACME directory":    strings.Replace(validJSON, `https://acme-v02`, `http://acme-v02`, 1),
		"repo URL with newline":  strings.Replace(validJSON, `php/"`, `php/\nX-Injected: 1"`, 1),
		"repo URL with userinfo": strings.Replace(validJSON, `https://packages`, `https://u:p@packages`, 1),
		"PHP not on allowlist":   strings.Replace(validJSON, `"default_php":"8.5"`, `"default_php":"8.1"`, 1),
		"retention 0":            strings.Replace(validJSON, `"log_retention_days":14`, `"log_retention_days":0`, 1),
		"cap below 1 GiB":        strings.Replace(validJSON, `5368709120`, `1024`, 1),
		"relative CA bundle":     strings.Replace(validJSON, `{`, `{"acme_ca_bundle":"pebble.pem",`, 1),
		"email with a name":      strings.Replace(validJSON, `{`, `{"acme_email":"Admin <admin@example.com>",`, 1),
		"email with a newline":   strings.Replace(validJSON, `{`, `{"acme_email":"admin@example.com\nx",`, 1),
		"uppercase key":          strings.Replace(validJSON, `"panel_php"`, `"Panel_php"`, 1),
		"duplicate key":          strings.Replace(validJSON, `{`, `{"panel_php":"8.5",`, 1),
	} {
		p := paths.Paths{Root: t.TempDir()}
		writeConfig(t, p, body)
		if _, err := Load(p); err == nil {
			t.Errorf("%s: Load accepted it", name)
		}
	}
}

func TestConfigLoadMissingFile(t *testing.T) {
	if _, err := Load(paths.Paths{Root: t.TempDir()}); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("Load of a missing file = %v, want ErrNotExist", err)
	}
}
