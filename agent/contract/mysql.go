package contract

import (
	"errors"
	"regexp"
	"slices"
	"strings"
)

// The database commands (v0.3.0 plan, "New agent commands"). Each job runs
// under its site's subject, so two jobs of one site never overlap; the
// installer runs under the server subject.
func init() {
	Register(Descriptor{
		Name:    "mysql.install",
		Kind:    KindJob,
		Audited: true,
		Params:  func() Params { return &MySQLInstallParams{} },
		Result:  ServiceInstallResult{},
		Subject: func(Params) string { return "server" },
	})
	Register(Descriptor{
		Name:   "db.list",
		Kind:   KindRead,
		Params: func() Params { return &DbListParams{} },
		Result: DbListResult{},
	})
	Register(Descriptor{
		Name:    "db.create",
		Kind:    KindJob,
		Audited: true,
		Params:  func() Params { return &DbCreateParams{} },
		Result:  DatabaseResult{},
		Subject: func(p Params) string { return "site:" + string(p.(*DbCreateParams).Site) },
	})
	Register(Descriptor{
		Name:    "db.drop",
		Kind:    KindJob,
		Audited: true,
		Params:  func() Params { return &DbDropParams{} },
		Result:  DatabaseResult{},
		Subject: func(p Params) string { return "site:" + string(p.(*DbDropParams).Site) },
	})
	// The password is a secret: the audit log and agent_actions redact it.
	Register(Descriptor{
		Name:    "db.user",
		Kind:    KindJob,
		Audited: true,
		Params:  func() Params { return &DbUserParams{} },
		Result:  ServiceUserResult{},
		Subject: func(p Params) string { return "site:" + string(p.(*DbUserParams).Site) },
	})
}

// RE2's $ matches only at the very end of the text; PHP mirrors of these
// patterns anchor with \z.
var (
	databaseNameRe    = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)
	servicePasswordRe = regexp.MustCompile(`^[A-Za-z0-9_-]{24,64}$`)
)

// reservedDatabaseNames are MySQL's own schemas and Zimam's.
var reservedDatabaseNames = []string{"mysql", "sys", "information_schema", "performance_schema", "zimam"}

// DatabaseName is a MySQL database name. The pattern has no backtick, quote,
// space or semicolon, so the agent can put it in backquotes in SQL.
type DatabaseName string

func (n DatabaseName) Validate() error {
	if !databaseNameRe.MatchString(string(n)) {
		return errors.New("must be 1–64 lowercase letters, digits or underscores and start with a letter")
	}
	if slices.Contains(reservedDatabaseNames, string(n)) {
		return errors.New("is reserved")
	}
	return nil
}

// maxGrantNameLength is MySQL's limit on a database name in a GRANT or
// REVOKE, where every underscore is written with a backslash.
const maxGrantNameLength = 64

func (DatabaseName) JSONSchema() Schema {
	return Schema{"type": "string", "pattern": databaseNameRe.String(), "not": Schema{"enum": reservedDatabaseNames}}
}

// DatabasePrefix is what every database of a site starts with: "s_", the
// site name with each hyphen turned into an underscore, and "_". It is also
// the start of the site's MySQL user name. A prefix alone does not identify
// a site: see BelongsTo.
func DatabasePrefix(site SiteName) string {
	return "s_" + strings.ReplaceAll(string(site), "-", "_") + "_"
}

// BelongsTo reports whether the name carries the site's prefix and has at
// least one more character. That is necessary for a database of the site,
// but not sufficient: when one site's name extends another's (shop and
// shop-x), a name such as s_shop_x_main carries both prefixes. The contract
// cannot know which sites exist, so handlers decide from the sites' state:
// OwnerAmong names the site a name belongs to.
func (n DatabaseName) BelongsTo(site SiteName) bool {
	rest, ok := strings.CutPrefix(string(n), DatabasePrefix(site))
	return ok && rest != ""
}

// ServicePassword is a password for a site's MySQL or Valkey user. The
// panel generates it. Its alphabet needs no escaping in a single-quoted SQL
// literal or an ACL rule. Fields of this type are tagged secret:"true".
type ServicePassword string

func (p ServicePassword) Validate() error {
	if !servicePasswordRe.MatchString(string(p)) {
		return errors.New("must be 24–64 letters, digits, underscores or hyphens")
	}
	return nil
}

func (ServicePassword) JSONSchema() Schema {
	return Schema{"type": "string", "pattern": servicePasswordRe.String()}
}

// MySQLInstallParams is mysql.install's params: none.
type MySQLInstallParams struct{}

func (MySQLInstallParams) Validate() error { return nil }

// ServiceInstallResult is the version of the service mysql.install and
// valkey.install installed, as its package reports it.
type ServiceInstallResult struct {
	Version string `json:"version"`
}

// DbListParams is db.list's params. Without a site it lists every
// database.
type DbListParams struct {
	Site SiteName `json:"site,omitempty"`
}

func (p *DbListParams) Validate() error {
	f := FieldErrors{}
	if p.Site != "" {
		f.Check("site", p.Site)
	}
	return f.Err()
}

// DatabaseInfo is one database, the site it belongs to and its size.
type DatabaseInfo struct {
	Name      DatabaseName `json:"name"`
	Site      SiteName     `json:"site"`
	SizeBytes DatabaseSize `json:"size_bytes"`
}

// DatabaseSize is a database's size in bytes.
type DatabaseSize int64

func (s DatabaseSize) Validate() error {
	if s < 0 {
		return errors.New("must not be negative")
	}
	return nil
}

func (DatabaseSize) JSONSchema() Schema {
	return Schema{"type": "integer", "minimum": 0}
}

// DbListResult is db.list's result. The list is empty, never null: the
// schema expects an array.
type DbListResult struct {
	Databases []DatabaseInfo `json:"databases"`
}

// NewDbListResult is a result with an empty list, which encodes as [].
func NewDbListResult() DbListResult { return DbListResult{Databases: []DatabaseInfo{}} }

// checkDatabase checks a site and one of its databases together, for the
// params that carry both. It refuses a name without the site's prefix, and
// nothing more: a name that another site's prefix also covers passes here,
// and the handler refuses it from the sites' state.
func checkDatabase(f FieldErrors, site SiteName, db DatabaseName) {
	f.Check("site", site)
	f.Check("database", db)
	if f["database"] == "" && site.Validate() == nil && !db.BelongsTo(site) {
		f["database"] = "must start with " + DatabasePrefix(site) + " and have at least one more character"
	}
	// A pattern can't count underscores twice, so the schema doesn't show
	// this: the grant that gives the site's user the database writes each
	// underscore as two characters, and MySQL refuses a name over 64.
	if f["database"] == "" && len(db)+strings.Count(string(db), "_") > maxGrantNameLength {
		f["database"] = "is too long: each underscore counts twice, and the length with them may be at most 64"
	}
}

// DbCreateParams is db.create's params: a database of the site, named with
// the site's prefix.
type DbCreateParams struct {
	Site     SiteName     `json:"site"`
	Database DatabaseName `json:"database"`
}

func (p *DbCreateParams) Validate() error {
	f := FieldErrors{}
	checkDatabase(f, p.Site, p.Database)
	return f.Err()
}

// DbDropParams is db.drop's params: a database of the site.
type DbDropParams struct {
	Site     SiteName     `json:"site"`
	Database DatabaseName `json:"database"`
}

func (p *DbDropParams) Validate() error {
	f := FieldErrors{}
	checkDatabase(f, p.Site, p.Database)
	return f.Err()
}

// DatabaseResult is the database db.create made or db.drop removed.
type DatabaseResult struct {
	Site     SiteName     `json:"site"`
	Database DatabaseName `json:"database"`
}

// DbUserParams is db.user's params: the site's MySQL user and the password
// it gets, whether the user is new or rotated.
type DbUserParams struct {
	Site     SiteName        `json:"site"`
	Password ServicePassword `json:"password" secret:"true"`
}

func (p *DbUserParams) Validate() error {
	f := FieldErrors{}
	f.Check("site", p.Site)
	f.Check("password", p.Password)
	return f.Err()
}

// ServiceUserResult is the site whose MySQL or Valkey user db.user or
// cache.user set. The password is never echoed.
type ServiceUserResult struct {
	Site SiteName `json:"site"`
}

// ClaimedBy returns every site in the list whose prefix the name carries,
// in list order. A handler refuses a name that is claimed by a site other
// than the one it acts for.
func (n DatabaseName) ClaimedBy(sites []SiteName) []SiteName {
	var out []SiteName
	for _, s := range sites {
		if n.BelongsTo(s) {
			out = append(out, s)
		}
	}
	return out
}

// OwnerAmong returns the site in the list that a database name belongs to:
// of the sites whose prefix the name carries, the one with the longest
// prefix. With shop and shop-x, s_shop_x_main belongs to shop-x and
// s_shop_main to shop. Two sites never have prefixes of one length that
// both fit a name, since distinct site names give distinct prefixes. The
// second result is false when no site's prefix fits.
func (n DatabaseName) OwnerAmong(sites []SiteName) (SiteName, bool) {
	var owner SiteName
	longest := 0
	for _, s := range sites {
		if n.BelongsTo(s) && len(DatabasePrefix(s)) > longest {
			owner, longest = s, len(DatabasePrefix(s))
		}
	}
	return owner, longest > 0
}
