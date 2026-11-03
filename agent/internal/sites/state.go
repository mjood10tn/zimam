// Package sites provisions sites and keeps each one's state file (design
// §9, D15). The agent has no database: whatever re-renders a site's files
// later reads them from its state file.
package sites

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"slices"
	"strings"

	"zimam/agent/contract"
	"zimam/agent/internal/paths"
)

// State is /var/lib/zimam/sites/<name>.json (§9.2 step 7).
type State struct {
	Name            contract.SiteName   `json:"name"`
	Domain          contract.Domain     `json:"domain"`
	Type            contract.SiteType   `json:"type"`
	PHPVersion      contract.PHPVersion `json:"php_version,omitempty"` // PHP sites only
	TemplateVersion int                 `json:"template_version"`
	Security        Security            `json:"security"`
}

// Security is a site's security settings (§9.6). site.security changes
// them from Task 33 on; until then every site has the defaults. Password
// protection's users live in the site's htpasswd, not here.
type Security struct {
	ForceHTTPS            bool            `json:"force_https"`
	HSTSMaxAge            int             `json:"hsts_max_age"` // 0 is off
	HSTSIncludeSubdomains bool            `json:"hsts_include_subdomains"`
	HeadersPreset         bool            `json:"headers_preset"`
	BlockRiskyFunctions   bool            `json:"block_risky_functions"`
	RestrictToSiteFolder  bool            `json:"restrict_to_site_folder"`
	IPAllow               contract.IPList `json:"ip_allow"`
	IPDeny                contract.IPList `json:"ip_deny"`
	PasswordProtection    bool            `json:"password_protection"`
}

// DefaultSecurity is §9.6's defaults: the header preset on, everything else
// off or empty. force_https turns on once a certificate exists (Task 30).
func DefaultSecurity() Security {
	return Security{HeadersPreset: true, IPAllow: contract.IPList{}, IPDeny: contract.IPList{}}
}

func (s State) validate() error {
	f := contract.FieldErrors{}
	f.Check("name", s.Name)
	f.Check("domain", s.Domain)
	f.Check("type", s.Type)
	if s.Type == contract.SitePHP {
		f.Check("php_version", s.PHPVersion)
	}
	return f.Err()
}

// Load reads a site's state file strictly. A site without one is
// fs.ErrNotExist.
func Load(p paths.Paths, name contract.SiteName) (State, error) {
	if err := name.Validate(); err != nil {
		return State{}, fmt.Errorf("site %q %v", name, err)
	}
	data, err := os.ReadFile(p.SiteState(name))
	if err != nil {
		return State{}, err
	}
	var st State
	if err := contract.DecodeStrict(data, &st); err != nil {
		return State{}, fmt.Errorf("%s: %w", p.SiteState(name), err)
	}
	if err := st.validate(); err != nil {
		return State{}, fmt.Errorf("%s: %w", p.SiteState(name), err)
	}
	if st.Name != name {
		return State{}, fmt.Errorf("%s holds site %q", p.SiteState(name), st.Name)
	}
	return st, nil
}

// Save writes a site's state file atomically, root 0600 in a 0700
// directory (§5.9).
func Save(p paths.Paths, st State) error {
	if err := st.validate(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	dir := p.SitesStateDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".site-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // fails harmlessly once renamed
	if _, err := tmp.Write(append(data, '\n')); err != nil {
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
	return os.Rename(tmp.Name(), p.SiteState(st.Name))
}

// All reads every site's state file, sorted by name.
func All(p paths.Paths) ([]State, error) {
	entries, err := os.ReadDir(p.SitesStateDir())
	if errors.Is(err, fs.ErrNotExist) {
		return []State{}, nil
	}
	if err != nil {
		return nil, err
	}
	all := []State{}
	for _, e := range entries {
		name, ok := strings.CutSuffix(e.Name(), ".json")
		if !ok || !e.Type().IsRegular() || strings.HasPrefix(name, ".") {
			continue
		}
		st, err := Load(p, contract.SiteName(name))
		if err != nil {
			return nil, err
		}
		all = append(all, st)
	}
	slices.SortFunc(all, func(a, b State) int { return strings.Compare(string(a.Name), string(b.Name)) })
	return all, nil
}
