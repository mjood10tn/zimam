package contract

import (
	"errors"
	"fmt"
	"net/netip"
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// domainLabel is one DNS label, without anchors, so the Domain schema can be
// built from it.
const domainLabel = `[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?`

// RE2's $ matches only at the very end of the text, so a trailing newline
// can't slip past these patterns. PHP's $ does allow one: mirror them in
// PHP with \z (global rule 14).
var (
	siteNameRe    = regexp.MustCompile(`^[a-z][a-z0-9-]{0,22}[a-z0-9]$`)
	domainLabelRe = regexp.MustCompile(`^` + domainLabel + `$`)
	lettersRe     = regexp.MustCompile(`^[a-z]+$`)
	authUserRe    = regexp.MustCompile(`^[A-Za-z0-9._-]{1,32}$`)
	bcryptRe      = regexp.MustCompile(`^\$2y\$1[0-4]\$[./A-Za-z0-9]{53}$`)
	ulidRe        = regexp.MustCompile(`^[0-9A-HJKMNP-TV-Z]{26}$`)
	archiveRe     = regexp.MustCompile(`^[a-z0-9-]+\.log-[0-9]{8}-[0-9]+\.gz$`)
)

var reservedSiteNames = []string{"zimam", "panel", "default", "acme"}

type SiteName string

func (n SiteName) Validate() error {
	if !siteNameRe.MatchString(string(n)) {
		return errors.New("must be 2–24 lowercase letters, digits or hyphens, start with a letter and not end with a hyphen")
	}
	if slices.Contains(reservedSiteNames, string(n)) {
		return errors.New("is reserved")
	}
	return nil
}

func (SiteName) JSONSchema() Schema {
	return Schema{"type": "string", "pattern": siteNameRe.String(), "not": Schema{"enum": reservedSiteNames}}
}

type Domain string

func (d Domain) Validate() error {
	s := string(d)
	if len(s) > 253 {
		return errors.New("must be at most 253 characters")
	}
	labels := strings.Split(s, ".")
	if len(labels) < 2 {
		return errors.New("must have at least two labels")
	}
	for _, l := range labels {
		if !domainLabelRe.MatchString(l) {
			return errors.New("must be lowercase ASCII labels of letters, digits and inner hyphens")
		}
	}
	if tld := labels[len(labels)-1]; !lettersRe.MatchString(tld) && !strings.HasPrefix(tld, "xn--") {
		return errors.New("must end in a letters-only or xn-- label")
	}
	return nil
}

// JSONSchema states Validate as one pattern: one or more labels with a dot,
// then a last label that is letters only or an xn-- label (both at most 63
// characters).
func (Domain) JSONSchema() Schema {
	return Schema{
		"type":      "string",
		"maxLength": 253,
		"pattern":   `^(` + domainLabel + `\.)+([a-z]{1,63}|xn--[a-z0-9-]{0,58}[a-z0-9])$`,
	}
}

type IPList []string

func (l IPList) Validate() error {
	if len(l) > 100 {
		return errors.New("must have at most 100 entries")
	}
	for i, e := range l {
		if _, err := ParseIPEntry(e); err != nil {
			return fmt.Errorf("entry %d %v", i+1, err)
		}
	}
	return nil
}

// JSONSchema checks only the shape of each entry. Canonical form (D21) needs
// net/netip, so only Validate enforces it.
func (IPList) JSONSchema() Schema {
	return Schema{
		"type":     "array",
		"maxItems": 100,
		"items":    Schema{"type": "string", "pattern": `^[0-9A-Fa-f.:]+(/[0-9]{1,3})?$`},
	}
}

// ParseIPEntry accepts an address or a CIDR prefix in canonical form (D21).
// Templates render the returned prefix, never the raw entry.
func ParseIPEntry(e string) (netip.Prefix, error) {
	if strings.Contains(e, "/") {
		p, err := netip.ParsePrefix(e)
		if err != nil || p.Addr().Is4In6() {
			return netip.Prefix{}, errors.New("is not a valid CIDR prefix")
		}
		if p != p.Masked() {
			return netip.Prefix{}, fmt.Errorf("has host bits set; use %s", p.Masked())
		}
		return p, nil
	}
	a, err := netip.ParseAddr(e)
	if err != nil || a.Zone() != "" || a.Is4In6() {
		return netip.Prefix{}, errors.New("is not a valid IP address")
	}
	return netip.PrefixFrom(a, a.BitLen()), nil
}

type BasicAuthUser struct {
	Username     string `json:"username"`
	PasswordHash string `json:"password_hash" secret:"true"`
}

type BasicAuthUsers []BasicAuthUser

func (u BasicAuthUsers) Validate() error {
	if len(u) > 20 {
		return errors.New("must have at most 20 users")
	}
	seen := map[string]bool{}
	for i, x := range u {
		if !authUserRe.MatchString(x.Username) {
			return fmt.Errorf("user %d: username must be 1–32 letters, digits, dots, underscores or hyphens", i+1)
		}
		if seen[x.Username] {
			return fmt.Errorf("user %d: duplicate username", i+1)
		}
		seen[x.Username] = true
		if !bcryptRe.MatchString(x.PasswordHash) {
			return fmt.Errorf("user %d: password hash must be bcrypt ($2y$, cost 10–14)", i+1)
		}
	}
	return nil
}

// JSONSchema can't express unique usernames; only Validate checks them.
func (BasicAuthUsers) JSONSchema() Schema {
	return Schema{
		"type":     "array",
		"maxItems": 20,
		"items": Schema{
			"type":                 "object",
			"additionalProperties": false,
			"required":             []string{"username", "password_hash"},
			"properties": map[string]Schema{
				"username":      {"type": "string", "pattern": authUserRe.String()},
				"password_hash": {"type": "string", "pattern": bcryptRe.String(), "x-zimam-secret": true},
			},
		},
	}
}

type HSTSMaxAge int

var hstsMaxAges = []HSTSMaxAge{86400, 2592000, 31536000}

func (a HSTSMaxAge) Validate() error {
	if slices.Contains(hstsMaxAges, a) {
		return nil
	}
	return errors.New("must be 86400, 2592000 or 31536000")
}

func (HSTSMaxAge) JSONSchema() Schema {
	return Schema{"type": "integer", "enum": hstsMaxAges}
}

type JobID string

func (j JobID) Validate() error {
	if !ulidRe.MatchString(string(j)) {
		return errors.New("must be a ULID")
	}
	return nil
}

func (JobID) JSONSchema() Schema {
	return Schema{"type": "string", "pattern": ulidRe.String()}
}

type LogSource string

var fixedLogSources = []string{
	"site.nginx-access", "site.nginx-error", "site.php-error",
	"journal.system", "journal.kernel", "journal.ssh", "journal.nginx",
	"journal.zimam-agent", "journal.zimam-panel",
	"file.nginx-error", "file.certbot", "file.apt-history",
	"file.unattended-upgrades", "file.zimam-audit",
}

func (s LogSource) Validate() error {
	if slices.Contains(fixedLogSources, string(s)) {
		return nil
	}
	if v, ok := strings.CutPrefix(string(s), "journal.php-"); ok && PHPVersion(v).Validate() == nil {
		return nil
	}
	return errors.New("is not a known log source")
}

func (LogSource) JSONSchema() Schema {
	ids := slices.Clone(fixedLogSources)
	for _, r := range PHPAllowlist {
		ids = append(ids, "journal.php-"+string(r.Version))
	}
	return Schema{"type": "string", "enum": ids}
}

// IsSite reports whether the source needs a site name.
func (s LogSource) IsSite() bool { return strings.HasPrefix(string(s), "site.") }

// ArchiveName checks the shape only. Handlers must also find the name in the
// agent's own listing of that source (§5.4).
type ArchiveName string

func (a ArchiveName) Validate() error {
	if !archiveRe.MatchString(string(a)) {
		return errors.New("is not a log archive name")
	}
	return nil
}

func (ArchiveName) JSONSchema() Schema {
	return Schema{"type": "string", "pattern": archiveRe.String()}
}

type LogSearch string

const maxLogSearchRunes = 200

func (q LogSearch) Validate() error {
	s := string(q)
	if !utf8.ValidString(s) || utf8.RuneCountInString(s) > maxLogSearchRunes {
		return errors.New("must be at most 200 characters of valid UTF-8")
	}
	if strings.IndexFunc(s, unicode.IsControl) >= 0 {
		return errors.New("must not contain control characters")
	}
	return nil
}

// JSONSchema excludes the same runes as unicode.IsControl: U+0000–U+001F and
// U+007F–U+009F.
func (LogSearch) JSONSchema() Schema {
	return Schema{"type": "string", "maxLength": maxLogSearchRunes, "pattern": `^[^\x00-\x1f\x7f-\x9f]*$`}
}

const GiB = int64(1) << 30

type RetentionDays int

const (
	minRetentionDays = 1
	maxRetentionDays = 365
)

func (d RetentionDays) Validate() error {
	if d < minRetentionDays || d > maxRetentionDays {
		return errors.New("must be 1–365 days")
	}
	return nil
}

func (RetentionDays) JSONSchema() Schema {
	return Schema{"type": "integer", "minimum": minRetentionDays, "maximum": maxRetentionDays}
}

type SiteCapBytes int64

const maxSiteCapBytes = 100 * GiB

// minSiteCapBytes is 1 GiB (limits_release.go). e2e builds lower it to
// 1 KiB (limits_e2e.go) for the maintenance probe (§14.4).
func (c SiteCapBytes) Validate() error {
	if int64(c) < minSiteCapBytes || int64(c) > maxSiteCapBytes {
		return fmt.Errorf("must be between %d and %d bytes", minSiteCapBytes, maxSiteCapBytes)
	}
	return nil
}

func (SiteCapBytes) JSONSchema() Schema {
	return Schema{"type": "integer", "minimum": minSiteCapBytes, "maximum": maxSiteCapBytes}
}
