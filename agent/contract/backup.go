package contract

import (
	"errors"
	"fmt"
	"net/netip"
	"regexp"
	"slices"
	"strings"
	"time"
)

// The backup commands (v0.5.0 plan, "New agent commands"). Destinations and
// plans change server files (rclone.conf, timer units), so they run under the
// server subject. A run locks its plan, so a timer run and a panel run of one
// plan never overlap (R05-D9). A restore writes into one site's database or
// home, so it locks that site, like db.create and db.drop.
func init() {
	Register(Descriptor{
		Name:    "backup.destination",
		Kind:    KindJob,
		Audited: true,
		Params:  func() Params { return &BackupDestinationParams{} },
		Result:  BackupDestinationResult{},
		Subject: func(Params) string { return "server" },
	})
	Register(Descriptor{
		Name:    "backup.destination.delete",
		Kind:    KindJob,
		Audited: true,
		Params:  func() Params { return &BackupDestinationIDParams{} },
		Result:  BackupDestinationResult{},
		Subject: func(Params) string { return "server" },
	})
	Register(Descriptor{
		Name:    "backup.destination.test",
		Kind:    KindJob,
		Audited: true,
		Params:  func() Params { return &BackupDestinationIDParams{} },
		Result:  BackupDestinationTestResult{},
		Subject: func(Params) string { return "server" },
	})
	// A secret-returning read: audited, with the keys redacted (R05-D3, OQ2).
	Register(Descriptor{
		Name:    "backup.destination.keys",
		Kind:    KindRead,
		Audited: true,
		Params:  func() Params { return &BackupDestinationIDParams{} },
		Result:  BackupDestinationKeysResult{},
	})
	Register(Descriptor{
		Name:    "backup.plan",
		Kind:    KindJob,
		Audited: true,
		Params:  func() Params { return &BackupPlanParams{} },
		Result:  BackupPlanResult{},
		Subject: func(Params) string { return "server" },
	})
	Register(Descriptor{
		Name:    "backup.plan.delete",
		Kind:    KindJob,
		Audited: true,
		Params:  func() Params { return &BackupPlanIDParams{} },
		Result:  BackupPlanResult{},
		Subject: func(Params) string { return "server" },
	})
	Register(Descriptor{
		Name:    "backup.run",
		Kind:    KindJob,
		Audited: true,
		Params:  func() Params { return &BackupRunParams{} },
		Result:  BackupRunResult{},
		Subject: func(p Params) string { return "backup:" + string(p.(*BackupRunParams).Plan) },
	})
	Register(Descriptor{
		Name:   "backup.list",
		Kind:   KindRead,
		Params: func() Params { return &BackupPlanIDParams{} },
		Result: BackupListResult{},
	})
	Register(Descriptor{
		Name:   "backup.runs",
		Kind:   KindRead,
		Params: func() Params { return &BackupRunsParams{} },
		Result: BackupRunsResult{},
	})
	Register(Descriptor{
		Name:    "backup.restore",
		Kind:    KindJob,
		Audited: true,
		Params:  func() Params { return &BackupRestoreParams{} },
		Result:  BackupRestoreResult{},
		Subject: func(p Params) string { return "site:" + string(p.(*BackupRestoreParams).Site) },
	})
}

// portPattern is 1–65535 without leading zeros.
const portPattern = `([1-9][0-9]{0,3}|[1-5][0-9]{4}|6[0-4][0-9]{3}|65[0-4][0-9]{2}|655[0-2][0-9]|6553[0-5])`

// hostPattern is Domain's schema pattern without anchors.
const hostPattern = `(` + domainLabel + `\.)+([a-z]{1,63}|xn--[a-z0-9-]{0,58}[a-z0-9])`

// RE2's $ matches only at the very end of the text; PHP mirrors of these
// patterns anchor with \z.
var (
	backupIDNameRe     = regexp.MustCompile(`^[a-z][a-z0-9-]{0,22}[a-z0-9]$`)
	s3EndpointRe       = regexp.MustCompile(`^https://` + hostPattern + `(:` + portPattern + `)?$`)
	s3RegionRe         = regexp.MustCompile(`^[a-z0-9-]{1,32}$`)
	s3BucketRe         = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)
	s3BucketBadRe      = regexp.MustCompile(`\.\.|\.-|-\.|^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$`)
	s3CredentialRe     = regexp.MustCompile(`^[A-Za-z0-9/+=_.-]{1,256}$`)
	backupPathRe       = regexp.MustCompile(`^([a-z0-9_-]+(/[a-z0-9_-]+)*)?$`)
	sftpHostShapeRe    = regexp.MustCompile(`^(` + hostPattern + `|[0-9a-f.:]+)$`)
	sftpUserRe         = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)
	hostFingerprintRe  = regexp.MustCompile(`^SHA256:[A-Za-z0-9+/]{43}$`)
	encryptionSecretRe = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)
	backupScheduleRe   = regexp.MustCompile(`^(hourly|daily ([01][0-9]|2[0-3]):[0-5][0-9]|weekly (mon|tue|wed|thu|fri|sat|sun) ([01][0-9]|2[0-3]):[0-5][0-9])$`)
	backupIDRe         = regexp.MustCompile(`^[0-9]{8}T[0-9]{6}Z$`)
	backupPartRe       = regexp.MustCompile(`^(panel|db-[a-z][a-z0-9_]{0,63}|files-[a-z][a-z0-9-]{0,22}[a-z0-9])$`)
)

const (
	maxBackupPathLength = 128
	maxPlanSites        = 50
	maxPlanDatabases    = 100
	maxRetention        = 365
)

// backupName is the shape of a destination's or a plan's id. It ends up in
// rclone remote names, unit names and paths.
func backupName(s string) error {
	if !backupIDNameRe.MatchString(s) {
		return errors.New("must be 2–24 lowercase letters, digits or hyphens, start with a letter and not end with a hyphen")
	}
	return nil
}

// DestinationID names a backup destination: the crypt remote's name in
// rclone.conf, with "-raw" for the remote under it (R05-D2).
type DestinationID string

func (id DestinationID) Validate() error { return backupName(string(id)) }
func (DestinationID) JSONSchema() Schema {
	return Schema{"type": "string", "pattern": backupIDNameRe.String()}
}

// PlanID names a backup plan: its timer and service units, its lock, its
// folder on the destination and its run records.
type PlanID string

func (id PlanID) Validate() error { return backupName(string(id)) }
func (PlanID) JSONSchema() Schema {
	return Schema{"type": "string", "pattern": backupIDNameRe.String()}
}

// DestinationType is where backups go: S3-compatible storage or SFTP (OQ1).
type DestinationType string

const (
	DestinationS3   DestinationType = "s3"
	DestinationSFTP DestinationType = "sftp"
)

var destinationTypes = []DestinationType{DestinationS3, DestinationSFTP}

func (t DestinationType) Validate() error {
	if !slices.Contains(destinationTypes, t) {
		return errors.New("must be s3 or sftp")
	}
	return nil
}

func (DestinationType) JSONSchema() Schema { return Schema{"type": "string", "enum": destinationTypes} }

// S3Endpoint is https:// and a host name, with an optional port: no user,
// path, query or plain HTTP, so it renders into rclone.conf as it is.
type S3Endpoint string

func (e S3Endpoint) Validate() error {
	if !s3EndpointRe.MatchString(string(e)) {
		return errors.New("must be https:// and a lowercase host name, with an optional port and nothing else")
	}
	host, _, _ := strings.Cut(strings.TrimPrefix(string(e), "https://"), ":")
	return Domain(host).Validate()
}

// JSONSchema can't bound the host's length apart from the port's; only
// Validate checks the host is at most 253 characters.
func (S3Endpoint) JSONSchema() Schema {
	return Schema{"type": "string", "maxLength": len("https://") + 253 + len(":65535"), "pattern": s3EndpointRe.String()}
}

// S3Region is the bucket's region as the provider names it.
type S3Region string

func (r S3Region) Validate() error {
	if !s3RegionRe.MatchString(string(r)) {
		return errors.New("must be 1–32 lowercase letters, digits or hyphens")
	}
	return nil
}

func (S3Region) JSONSchema() Schema { return Schema{"type": "string", "pattern": s3RegionRe.String()} }

// S3Bucket follows S3's bucket naming rules: 3–63 lowercase letters, digits,
// dots and hyphens, starting and ending with a letter or digit, without two
// dots or a dot next to a hyphen, and not shaped like an IP address.
type S3Bucket string

func (b S3Bucket) Validate() error {
	if !s3BucketRe.MatchString(string(b)) || s3BucketBadRe.MatchString(string(b)) {
		return errors.New("must follow S3's bucket naming rules")
	}
	return nil
}

func (S3Bucket) JSONSchema() Schema {
	return Schema{"type": "string", "pattern": s3BucketRe.String(), "not": Schema{"pattern": s3BucketBadRe.String()}}
}

// S3Credential is an access key id or secret access key. Its alphabet covers
// the providers' keys and holds nothing that ends or comments a line of
// rclone.conf. Fields of this type are secret.
type S3Credential string

func (c S3Credential) Validate() error {
	if !s3CredentialRe.MatchString(string(c)) {
		return errors.New("must be 1–256 letters, digits or / + = _ . -")
	}
	return nil
}

func (S3Credential) JSONSchema() Schema {
	return Schema{"type": "string", "pattern": s3CredentialRe.String()}
}

// BackupPath is a bucket prefix or an SFTP folder relative to the login's
// home: lowercase segments joined by single slashes, with no dot at all, so
// no "..", no leading or trailing slash. Empty means the top.
type BackupPath string

func (p BackupPath) Validate() error {
	if len(p) > maxBackupPathLength || !backupPathRe.MatchString(string(p)) {
		return errors.New("must be at most 128 characters of lowercase segments (letters, digits, _ and -) joined by single slashes")
	}
	return nil
}

func (BackupPath) JSONSchema() Schema {
	return Schema{"type": "string", "maxLength": maxBackupPathLength, "pattern": backupPathRe.String()}
}

// SFTPHost is a host name, or an IP address in canonical form.
type SFTPHost string

func (h SFTPHost) Validate() error {
	if Domain(h).Validate() == nil {
		return nil
	}
	a, err := netip.ParseAddr(string(h))
	if err != nil || a.Zone() != "" || a.Is4In6() || a.String() != string(h) {
		return errors.New("must be a lowercase host name or an IP address in canonical form")
	}
	return nil
}

// JSONSchema checks only the shape; canonical IP form needs net/netip.
func (SFTPHost) JSONSchema() Schema {
	return Schema{"type": "string", "maxLength": 253, "pattern": sftpHostShapeRe.String()}
}

// TCPPort is 1–65535.
type TCPPort int

func (p TCPPort) Validate() error {
	if p < 1 || p > 65535 {
		return errors.New("must be 1–65535")
	}
	return nil
}

func (TCPPort) JSONSchema() Schema { return Schema{"type": "integer", "minimum": 1, "maximum": 65535} }

// SFTPUser is the login on the SFTP server.
type SFTPUser string

func (u SFTPUser) Validate() error {
	if !sftpUserRe.MatchString(string(u)) {
		return errors.New("must be 1–32 lowercase letters, digits, underscores or hyphens and start with a letter or underscore")
	}
	return nil
}

func (SFTPUser) JSONSchema() Schema { return Schema{"type": "string", "pattern": sftpUserRe.String()} }

// HostKeyFingerprint is an SSH host key's SHA-256 fingerprint as ssh-keygen
// -l prints it (OQ3).
type HostKeyFingerprint string

func (f HostKeyFingerprint) Validate() error {
	if !hostFingerprintRe.MatchString(string(f)) {
		return errors.New("must be SHA256: and 43 base64 characters, as ssh-keygen -l prints it")
	}
	return nil
}

func (HostKeyFingerprint) JSONSchema() Schema {
	return Schema{"type": "string", "pattern": hostFingerprintRe.String()}
}

// EncryptionSecret is a crypt password or salt: 32 random bytes the panel
// generates, base64url without padding (R05-D3). Fields of this type are
// secret.
type EncryptionSecret string

func (s EncryptionSecret) Validate() error {
	if !encryptionSecretRe.MatchString(string(s)) {
		return errors.New("must be 43 base64url characters")
	}
	return nil
}

func (EncryptionSecret) JSONSchema() Schema {
	return Schema{"type": "string", "pattern": encryptionSecretRe.String()}
}

// S3Settings are an S3-compatible destination's fields.
type S3Settings struct {
	Endpoint        S3Endpoint   `json:"endpoint"`
	Region          S3Region     `json:"region"`
	Bucket          S3Bucket     `json:"bucket"`
	Prefix          BackupPath   `json:"prefix,omitempty"`
	AccessKeyID     S3Credential `json:"access_key_id" secret:"true"`
	SecretAccessKey S3Credential `json:"secret_access_key" secret:"true"`
}

func (s *S3Settings) check(f FieldErrors, prefix string) {
	f.Check(prefix+"endpoint", s.Endpoint)
	f.Check(prefix+"region", s.Region)
	f.Check(prefix+"bucket", s.Bucket)
	f.Check(prefix+"prefix", s.Prefix)
	f.Check(prefix+"access_key_id", s.AccessKeyID)
	f.Check(prefix+"secret_access_key", s.SecretAccessKey)
}

func (s S3Settings) Validate() error {
	f := FieldErrors{}
	s.check(f, "")
	return f.Err()
}

// SFTPSettings are an SFTP destination's fields. The agent makes the key
// pair (R05-D2); a given fingerprint is the only host key the first test
// accepts (OQ3).
type SFTPSettings struct {
	Host               SFTPHost           `json:"host"`
	Port               TCPPort            `json:"port"`
	User               SFTPUser           `json:"user"`
	Path               BackupPath         `json:"path,omitempty"`
	HostKeyFingerprint HostKeyFingerprint `json:"host_key_fingerprint,omitempty"`
}

func (s *SFTPSettings) check(f FieldErrors, prefix string) {
	f.Check(prefix+"host", s.Host)
	f.Check(prefix+"port", s.Port)
	f.Check(prefix+"user", s.User)
	f.Check(prefix+"path", s.Path)
	if s.HostKeyFingerprint != "" {
		f.Check(prefix+"host_key_fingerprint", s.HostKeyFingerprint)
	}
}

// BackupDestinationParams is backup.destination's params: a new destination
// or new settings for an existing one. Exactly the settings of its type are
// given. The keys are the panel's, shown to the owner once (R05-D3): a new
// destination needs both, and an existing one keeps its own, since its
// backups can't be read without them. The panel never stores them, so it
// leaves them out of an edit; the handler refuses keys that differ.
type BackupDestinationParams struct {
	ID                 DestinationID    `json:"id"`
	Type               DestinationType  `json:"type"`
	S3                 *S3Settings      `json:"s3,omitempty"`
	SFTP               *SFTPSettings    `json:"sftp,omitempty"`
	EncryptionPassword EncryptionSecret `json:"encryption_password,omitempty" secret:"true"`
	EncryptionSalt     EncryptionSecret `json:"encryption_salt,omitempty" secret:"true"`
}

// HasKeys reports whether the params carry the crypt keys.
func (p *BackupDestinationParams) HasKeys() bool { return p.EncryptionPassword != "" }

func (p *BackupDestinationParams) Validate() error {
	f := FieldErrors{}
	f.Check("id", p.ID)
	f.Check("type", p.Type)
	switch {
	case p.Type == DestinationS3 && p.S3 == nil:
		f["s3"] = "is required for an s3 destination"
	case p.Type == DestinationSFTP && p.SFTP == nil:
		f["sftp"] = "is required for an sftp destination"
	}
	if p.S3 != nil {
		if p.Type != DestinationS3 {
			f["s3"] = "is only for an s3 destination"
		}
		p.S3.check(f, "s3.")
	}
	if p.SFTP != nil {
		if p.Type != DestinationSFTP {
			f["sftp"] = "is only for an sftp destination"
		}
		p.SFTP.check(f, "sftp.")
	}
	if p.EncryptionPassword != "" || p.EncryptionSalt != "" {
		f.Check("encryption_password", p.EncryptionPassword)
		f.Check("encryption_salt", p.EncryptionSalt)
	}
	if p.EncryptionSalt != "" && p.EncryptionSalt == p.EncryptionPassword {
		f["encryption_salt"] = "must differ from the password"
	}
	return f.Err()
}

// BackupDestinationIDParams names one destination.
type BackupDestinationIDParams struct {
	ID DestinationID `json:"id"`
}

func (p *BackupDestinationIDParams) Validate() error {
	f := FieldErrors{}
	f.Check("id", p.ID)
	return f.Err()
}

// BackupDestinationResult is the destination a job set up or removed. For a
// new SFTP destination it carries the public key the owner installs on the
// server (R05-D2).
type BackupDestinationResult struct {
	ID        DestinationID `json:"id"`
	PublicKey string        `json:"public_key,omitempty"`
}

// BackupDestinationTestResult is a passed test: a probe object was written,
// listed and deleted through the crypt remote. For SFTP it carries the
// pinned host key's fingerprint (OQ3).
type BackupDestinationTestResult struct {
	ID                 DestinationID `json:"id"`
	HostKeyFingerprint string        `json:"host_key_fingerprint,omitempty"`
}

// BackupDestinationKeysResult is a destination's crypt keys, read back from
// rclone.conf for "Show keys again" (OQ2).
type BackupDestinationKeysResult struct {
	ID                 DestinationID    `json:"id"`
	EncryptionPassword EncryptionSecret `json:"encryption_password" secret:"true"`
	EncryptionSalt     EncryptionSecret `json:"encryption_salt" secret:"true"`
}

// BackupSchedule is when a plan runs, in the server's time zone (R05-D7):
// "hourly", "daily HH:MM" or "weekly <mon…sun> HH:MM".
type BackupSchedule string

func (s BackupSchedule) Validate() error {
	if !backupScheduleRe.MatchString(string(s)) {
		return errors.New(`must be "hourly", "daily HH:MM" or "weekly <mon…sun> HH:MM"`)
	}
	return nil
}

func (BackupSchedule) JSONSchema() Schema {
	return Schema{"type": "string", "pattern": backupScheduleRe.String()}
}

// OnCalendar renders a valid schedule as a systemd OnCalendar= value.
func (s BackupSchedule) OnCalendar() string {
	f := strings.Fields(string(s))
	switch f[0] {
	case "daily":
		return "*-*-* " + f[1] + ":00"
	case "weekly":
		return strings.ToUpper(f[1][:1]) + f[1][1:] + " *-*-* " + f[2] + ":00"
	}
	return "hourly"
}

// Retention is how many backups a plan keeps (R05-D8): the last keep_last,
// plus the newest of each of the last keep_daily days, keep_weekly weeks and
// keep_monthly months.
type Retention struct {
	KeepLast    int `json:"keep_last"`
	KeepDaily   int `json:"keep_daily,omitempty"`
	KeepWeekly  int `json:"keep_weekly,omitempty"`
	KeepMonthly int `json:"keep_monthly,omitempty"`
}

func (r Retention) Validate() error {
	if r.KeepLast < 1 || r.KeepLast > maxRetention {
		return errors.New("keep_last must be 1–365")
	}
	for _, n := range []int{r.KeepDaily, r.KeepWeekly, r.KeepMonthly} {
		if n < 0 || n > maxRetention {
			return errors.New("keep_daily, keep_weekly and keep_monthly must be 0–365")
		}
	}
	return nil
}

func (Retention) JSONSchema() Schema {
	count := Schema{"type": "integer", "minimum": 0, "maximum": maxRetention}
	return Schema{
		"type":                 "object",
		"additionalProperties": false,
		"required":             []string{"keep_last"},
		"properties": map[string]Schema{
			"keep_last":    {"type": "integer", "minimum": 1, "maximum": maxRetention},
			"keep_daily":   count,
			"keep_weekly":  count,
			"keep_monthly": count,
		},
	}
}

// BackupPlanParams is backup.plan's params: a new plan or a plan's new
// settings. It backs up at least one thing. Databases may belong to any
// site; the handler checks they exist.
type BackupPlanParams struct {
	ID          PlanID         `json:"id"`
	Destination DestinationID  `json:"destination"`
	Sites       []SiteName     `json:"sites,omitempty"`
	Databases   []DatabaseName `json:"databases,omitempty"`
	Panel       bool           `json:"panel,omitempty"`
	Schedule    BackupSchedule `json:"schedule"`
	Retention   Retention      `json:"retention"`
}

func (p *BackupPlanParams) Validate() error {
	f := FieldErrors{}
	f.Check("id", p.ID)
	f.Check("destination", p.Destination)
	checkUnique(f, "sites", p.Sites, maxPlanSites)
	checkUnique(f, "databases", p.Databases, maxPlanDatabases)
	if len(p.Sites) == 0 && len(p.Databases) == 0 && !p.Panel {
		f["sites"] = "a plan must back up at least one site, database or the panel"
	}
	f.Check("schedule", p.Schedule)
	f.Check("retention", p.Retention)
	return f.Err()
}

// checkUnique checks a list of at most max valid, distinct entries.
func checkUnique[T interface {
	~string
	Validate() error
}](f FieldErrors, field string, list []T, max int) {
	if len(list) > max {
		f[field] = fmt.Sprintf("must have at most %d entries", max)
		return
	}
	for i, v := range list {
		if err := v.Validate(); err != nil {
			f[field] = fmt.Sprintf("entry %d %v", i+1, err)
			return
		}
		if slices.Contains(list[:i], v) {
			f[field] = fmt.Sprintf("entry %d is a duplicate", i+1)
			return
		}
	}
}

// BackupPlanIDParams names one plan.
type BackupPlanIDParams struct {
	Plan PlanID `json:"plan"`
}

func (p *BackupPlanIDParams) Validate() error {
	f := FieldErrors{}
	f.Check("plan", p.Plan)
	return f.Err()
}

// BackupPlanResult is the plan a job set up or removed.
type BackupPlanResult struct {
	ID PlanID `json:"id"`
}

// BackupRunParams is backup.run's params.
type BackupRunParams struct {
	Plan PlanID `json:"plan"`
}

func (p *BackupRunParams) Validate() error {
	f := FieldErrors{}
	f.Check("plan", p.Plan)
	return f.Err()
}

// BackupID names one backup of a plan: its UTC start, yyyymmddThhmmssZ
// (R05-D6). It is sortable and safe in a path.
type BackupID string

func (id BackupID) Validate() error {
	if !backupIDRe.MatchString(string(id)) {
		return errors.New("must be a UTC time as yyyymmddThhmmssZ")
	}
	return nil
}

func (BackupID) JSONSchema() Schema { return Schema{"type": "string", "pattern": backupIDRe.String()} }

// BackupPart is one object of a backup: "db-<database>", "files-<site>" or
// "panel" (the panel's own database).
type BackupPart string

const PanelPart BackupPart = "panel"

func (p BackupPart) Validate() error {
	if p == PanelPart {
		return nil
	}
	if db, ok := p.Database(); ok {
		return db.Validate()
	}
	if site, ok := p.Site(); ok {
		return site.Validate()
	}
	return errors.New(`must be "panel", "db-<database>" or "files-<site>"`)
}

func (BackupPart) JSONSchema() Schema {
	var reserved []string
	for _, n := range reservedDatabaseNames {
		reserved = append(reserved, "db-"+n)
	}
	for _, n := range reservedSiteNames {
		reserved = append(reserved, "files-"+n)
	}
	return Schema{"type": "string", "pattern": backupPartRe.String(), "not": Schema{"enum": reserved}}
}

// Database returns a database part's database.
func (p BackupPart) Database() (DatabaseName, bool) {
	db, ok := strings.CutPrefix(string(p), "db-")
	return DatabaseName(db), ok
}

// Site returns a files part's site.
func (p BackupPart) Site() (SiteName, bool) {
	s, ok := strings.CutPrefix(string(p), "files-")
	return SiteName(s), ok
}

// BackupPartInfo is one uploaded part: its size and the SHA-256 of what was
// uploaded (the compressed stream), as the manifest records them.
type BackupPartInfo struct {
	Part   BackupPart   `json:"part"`
	Bytes  DatabaseSize `json:"bytes"`
	SHA256 string       `json:"sha256"`
}

// BackupRunResult is a finished run: the backup and its parts.
type BackupRunResult struct {
	Plan   PlanID           `json:"plan"`
	Backup BackupID         `json:"backup"`
	Parts  []BackupPartInfo `json:"parts"`
}

// BackupInfo is one complete backup, read from its manifest.
type BackupInfo struct {
	Backup     BackupID         `json:"backup"`
	StartedAt  time.Time        `json:"started_at"`
	FinishedAt time.Time        `json:"finished_at"`
	Parts      []BackupPartInfo `json:"parts"`
}

// BackupListResult is a plan's complete backups, oldest first. The list is
// empty, never null.
type BackupListResult struct {
	Backups []BackupInfo `json:"backups"`
}

// BackupRunsParams is backup.runs' params: one plan's records or every
// plan's, only those after since when given.
type BackupRunsParams struct {
	Plan  PlanID   `json:"plan,omitempty"`
	Since BackupID `json:"since,omitempty"`
}

func (p *BackupRunsParams) Validate() error {
	f := FieldErrors{}
	if p.Plan != "" {
		f.Check("plan", p.Plan)
	}
	if p.Since != "" {
		f.Check("since", p.Since)
	}
	return f.Err()
}

// BackupTrigger is what started a run.
type BackupTrigger string

const (
	TriggerTimer BackupTrigger = "timer"
	TriggerPanel BackupTrigger = "panel"
)

func (BackupTrigger) JSONSchema() Schema {
	return Schema{"type": "string", "enum": []BackupTrigger{TriggerTimer, TriggerPanel}}
}

// BackupStatus is a run's state.
type BackupStatus string

const (
	BackupRunning   BackupStatus = "running"
	BackupSucceeded BackupStatus = "succeeded"
	BackupFailed    BackupStatus = "failed"
)

func (BackupStatus) JSONSchema() Schema {
	return Schema{"type": "string", "enum": []BackupStatus{BackupRunning, BackupSucceeded, BackupFailed}}
}

// BackupRunRecord is one run's record (R05-D9).
type BackupRunRecord struct {
	Plan       PlanID           `json:"plan"`
	Backup     BackupID         `json:"backup"`
	Trigger    BackupTrigger    `json:"trigger"`
	Status     BackupStatus     `json:"status"`
	Parts      []BackupPartInfo `json:"parts"`
	Error      string           `json:"error,omitempty"`
	StartedAt  time.Time        `json:"started_at"`
	FinishedAt *time.Time       `json:"finished_at,omitempty"`
}

// BackupRunsResult is the run records, oldest first. The list is empty,
// never null.
type BackupRunsResult struct {
	Runs []BackupRunRecord `json:"runs"`
}

// BackupRestoreParams is backup.restore's params (R05-D10). A database part
// restores into a database of the site: a new one, or with overwrite an
// existing one. A files part restores only the same site's files, into a
// restore folder in its home (OQ6). The panel's part is not restored by the
// agent (OQ8).
type BackupRestoreParams struct {
	Plan      PlanID       `json:"plan"`
	Backup    BackupID     `json:"backup"`
	Part      BackupPart   `json:"part"`
	Site      SiteName     `json:"site"`
	Database  DatabaseName `json:"database,omitempty"`
	Overwrite bool         `json:"overwrite,omitempty"`
}

func (p *BackupRestoreParams) Validate() error {
	f := FieldErrors{}
	f.Check("plan", p.Plan)
	f.Check("backup", p.Backup)
	f.Check("part", p.Part)
	f.Check("site", p.Site)
	if f["part"] != "" {
		return f.Err()
	}
	if site, ok := p.Part.Site(); ok {
		if site != p.Site {
			f["part"] = "belongs to another site"
		}
		if p.Database != "" {
			f["database"] = "is only for a database part"
		}
		if p.Overwrite {
			f["overwrite"] = "is only for a database part"
		}
		return f.Err()
	}
	if p.Part == PanelPart {
		f["part"] = "the panel's database isn't restored by the agent"
		return f.Err()
	}
	if p.Database == "" {
		f["database"] = "is required for a database part"
		return f.Err()
	}
	checkDatabase(f, p.Site, p.Database)
	return f.Err()
}

// BackupRestoreResult is what a restore wrote: the database, or the folder
// in the site's home the files went into. SkippedLinks are the archive's
// symlinks a files restore left out because they point outside the
// restore folder, or through another link (R05 Task 8), at most
// MaxSkippedLinks of them.
type BackupRestoreResult struct {
	Site         SiteName     `json:"site"`
	Part         BackupPart   `json:"part"`
	Database     DatabaseName `json:"database,omitempty"`
	Folder       string       `json:"folder,omitempty"`
	SkippedLinks []string     `json:"skipped_links,omitempty"`
}

// MaxSkippedLinks bounds BackupRestoreResult.SkippedLinks; the job log
// names every one.
const MaxSkippedLinks = 100
