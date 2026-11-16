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

func (DatabaseName) JSONSchema() Schema {
	return Schema{"type": "string", "pattern": databaseNameRe.String(), "not": Schema{"enum": reservedDatabaseNames}}
}

// DatabasePrefix is what every database of a site starts with: "s_", the
// site name with each hyphen turned into an underscore, and "_". It is also
// the start of the site's MySQL user name.
func DatabasePrefix(site SiteName) string {
	return "s_" + strings.ReplaceAll(string(site), "-", "_") + "_"
}

// BelongsTo reports whether the name is one of the site's databases: it
// starts with the site's prefix and has at least one more character.
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
	SizeBytes int64        `json:"size_bytes"`
}

// DbListResult is db.list's result. The list is empty, never null: the
// schema expects an array.
type DbListResult struct {
	Databases []DatabaseInfo `json:"databases"`
}

// NewDbListResult is a result with an empty list, which encodes as [].
func NewDbListResult() DbListResult { return DbListResult{Databases: []DatabaseInfo{}} }

// checkDatabase checks a site and one of its databases together, for the
// params that carry both.
func checkDatabase(f FieldErrors, site SiteName, db DatabaseName) {
	f.Check("site", site)
	f.Check("database", db)
	if f["database"] == "" && site.Validate() == nil && !db.BelongsTo(site) {
		f["database"] = "must start with " + DatabasePrefix(site) + " and have at least one more character"
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
