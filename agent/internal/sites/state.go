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
	"regexp"
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
	// Repository, Branch and DeployKeyFingerprint are set by site.repo
	// (v0.2.0); a site that never had one leaves them out.
	Repository           contract.Repository `json:"repository,omitempty"`
	Branch               contract.Branch     `json:"branch,omitempty"`
	DeployKeyFingerprint string              `json:"deploy_key_fingerprint,omitempty"`
	// KeepReleases and Releases are set by site.deploy: how many releases the
	// site keeps (0 is contract.DefaultKeepReleases), and the releases that
	// deploys built and that are still kept, oldest first, with the live one
	// marked Active. A provisioned site's "initial" is not one of them.
	KeepReleases contract.KeepReleases `json:"keep_releases,omitempty"`
	Releases     []contract.Release    `json:"releases,omitempty"`
	// Databases are the site's MySQL databases, sorted, set by db.create and
	// db.drop; a database belongs to the site whose state lists it. DBUser
	// is whether the site has its MySQL user, which db.create then grants
	// on the new database. A site that never had a database leaves both out.
	Databases []contract.DatabaseName `json:"databases,omitempty"`
	DBUser    bool                    `json:"db_user,omitempty"`
	// CacheUserHash is the SHA-256 (64 lowercase hex digits) of the password of
	// the site's Valkey user, set by cache.user. Valkey's ACL file is rendered
	// from it, so it is the only record of that password the agent keeps.
	CacheUserHash string `json:"cache_user_hash,omitempty"`
	// CachePurge is how far site.destroy got removing the site's Valkey keys when it
	// ran out of time or failed: which of its two passes, and the SCAN cursor to
	// continue from, so that running the command again makes progress however
	// large the keyspace is. It is cleared when the keys are gone, and by
	// cache.user, which starts the site's keys afresh.
	CachePurge *CachePurge `json:"cache_purge,omitempty"`
}

// KeyFingerprint is the shape of an OpenSSH SHA-256 key fingerprint:
// "SHA256:" and 32 bytes in unpadded base64. A site's deploy key
// fingerprint must have it, in the state file and in site.repo's result.
var KeyFingerprint = regexp.MustCompile(`^SHA256:[A-Za-z0-9+/]{43}$`)

// CachePurge is the saved progress of removing a site's Valkey keys.
type CachePurge struct {
	Pass   int    `json:"pass"`   // 1: before the user is removed, 2: after
	Cursor string `json:"cursor"` // the SCAN cursor to continue from, in digits
	// RunID is the Valkey process that gave the cursor, which is good only for
	// that process. A progress without one is rescanned from the start.
	RunID string `json:"run_id,omitempty"`
}

var purgeCursorRe = regexp.MustCompile(`^[0-9]{1,20}\z`)

var runIDRe = regexp.MustCompile(`^[0-9a-f]{40}\z`)

// cacheUserHashRe is a SHA-256 in lowercase hex, the form Valkey's ACL file
// takes after "#".
var cacheUserHashRe = regexp.MustCompile(`^[0-9a-f]{64}\z`)

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
	if s.Type.RunsPHP() {
		f.Check("php_version", s.PHPVersion)
	}
	if s.Repository != "" || s.Branch != "" || s.DeployKeyFingerprint != "" {
		f.Check("repository", s.Repository)
		f.Check("branch", s.Branch)
		if !KeyFingerprint.MatchString(s.DeployKeyFingerprint) {
			f["deploy_key_fingerprint"] = "must be an OpenSSH SHA-256 fingerprint"
		}
	}
	if s.KeepReleases != 0 {
		f.Check("keep_releases", s.KeepReleases)
	}
	if msg := checkReleases(s.Releases); msg != "" {
		f["releases"] = msg
	}
	if msg := s.checkDatabases(); msg != "" {
		f["databases"] = msg
	}
	if s.CacheUserHash != "" && !cacheUserHashRe.MatchString(s.CacheUserHash) {
		f["cache_user_hash"] = "must be 64 lowercase hex digits"
	}
	if c := s.CachePurge; c != nil {
		switch {
		case c.Pass != 1 && c.Pass != 2:
			f["cache_purge"] = "pass must be 1 or 2"
		case !purgeCursorRe.MatchString(c.Cursor):
			f["cache_purge"] = "cursor must be digits"
		case c.RunID != "" && !runIDRe.MatchString(c.RunID):
			f["cache_purge"] = "run_id must be 40 lowercase hex digits"
		case s.CacheUserHash == "":
			f["cache_purge"] = "needs a cache user hash"
		}
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

// Keep is how many releases the site keeps.
func (s State) Keep() contract.KeepReleases {
	if s.KeepReleases == 0 {
		return contract.DefaultKeepReleases
	}
	return s.KeepReleases
}

// checkReleases says what is wrong with a state file's releases, or "".
func checkReleases(rs []contract.Release) string {
	seen := map[contract.ReleaseID]bool{}
	active := 0
	for _, r := range rs {
		switch {
		case r.ID.Validate() != nil:
			return fmt.Sprintf("%q is not a release id", r.ID)
		case r.Commit.Validate() != nil:
			return fmt.Sprintf("release %s has a bad commit", r.ID)
		case r.Created.IsZero():
			return fmt.Sprintf("release %s has no creation time", r.ID)
		case seen[r.ID]:
			return fmt.Sprintf("release %s is listed twice", r.ID)
		}
		seen[r.ID] = true
		if r.Active {
			active++
		}
	}
	if active > 1 {
		return "more than one release is active"
	}
	return ""
}

// checkDatabases says what is wrong with the site's databases, or "": each
// must be a valid name with the site's prefix, and the list sorted without
// repeats, so that it has one form.
func (s State) checkDatabases() string {
	for i, n := range s.Databases {
		switch {
		case n.Validate() != nil:
			return fmt.Sprintf("%q is not a database name", n)
		case s.Name.Validate() == nil && !n.BelongsTo(s.Name):
			return fmt.Sprintf("%s doesn't start with %s", n, contract.DatabasePrefix(s.Name))
		case i > 0 && s.Databases[i-1] >= n:
			return fmt.Sprintf("%s is out of order or listed twice", n)
		}
	}
	return ""
}

// HasDatabase reports whether the site's state lists the database.
func (s State) HasDatabase(name contract.DatabaseName) bool {
	return slices.Contains(s.Databases, name)
}
