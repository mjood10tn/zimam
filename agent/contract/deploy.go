package contract

import (
	"errors"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

// The deploy commands (v0.2.0 plan, "New agent commands"). Every job runs
// under its site's subject, so a deploy and a rollback of one site never
// overlap (R02-D12).
func init() {
	Register(Descriptor{
		Name:    "site.repo",
		Kind:    KindJob,
		Audited: true,
		Params:  func() Params { return &SiteRepoParams{} },
		Result:  SiteRepoResult{},
		Subject: func(p Params) string { return "site:" + string(p.(*SiteRepoParams).Name) },
	})
	Register(Descriptor{
		Name:    "site.deploy",
		Kind:    KindJob,
		Audited: true,
		Params:  func() Params { return &SiteDeployParams{} },
		Result:  SiteDeployResult{},
		Subject: func(p Params) string { return "site:" + string(p.(*SiteDeployParams).Name) },
	})
	Register(Descriptor{
		Name:    "site.rollback",
		Kind:    KindJob,
		Audited: true,
		Params:  func() Params { return &SiteRollbackParams{} },
		Result:  SiteRollbackResult{},
		Subject: func(p Params) string { return "site:" + string(p.(*SiteRollbackParams).Name) },
	})
	Register(Descriptor{
		Name:   "site.releases",
		Kind:   KindRead,
		Params: func() Params { return &SiteNameParams{} },
		Result: SiteReleasesResult{},
	})
	// site.env.read is a read, but it returns a secret, so it is audited
	// (R02-D10), with the content redacted.
	Register(Descriptor{
		Name:    "site.env.read",
		Kind:    KindRead,
		Audited: true,
		Params:  func() Params { return &SiteNameParams{} },
		Result:  SiteEnvReadResult{},
	})
	Register(Descriptor{
		Name:    "site.env.write",
		Kind:    KindJob,
		Audited: true,
		Params:  func() Params { return &SiteEnvWriteParams{} },
		Result:  SiteNameResult{},
		Subject: func(p Params) string { return "site:" + string(p.(*SiteEnvWriteParams).Name) },
	})
}

// As in validate.go, RE2's $ matches only at the very end of the text; PHP
// mirrors of these patterns anchor with \z.
var (
	repositoryRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9-]{0,38}/[A-Za-z0-9._-]{1,100}$`)
	// .git in any case: spelled out, since JSON Schema has no (?i).
	repositoryForbiddenRe = regexp.MustCompile(`\.[gG][iI][tT]$|/\.\.?$`)
	branchRe              = regexp.MustCompile(`^[A-Za-z0-9._/-]{1,200}$`)
	// A leading - or /, "..", "//", a component starting with "." (the very
	// first one included), a trailing / or ".", or a component ending in
	// ".lock". "@{" needs an @, which branchRe already refuses.
	branchForbiddenRe = regexp.MustCompile(`^[-/]|\.\.|//|(^|/)\.|[/.]$|\.lock(/|$)`)
	commitSHARe       = regexp.MustCompile(`^[0-9a-f]{40}$`)
	releaseIDRe       = regexp.MustCompile(`^[0-9]{14}-[0-9a-f]{7}$`)
	noNULRe           = regexp.MustCompile(`^[^\x00]*$`)
)

// Repository is a GitHub repository as owner/name. The agent adds ".git"
// when it builds the remote, so the name never ends in it.
type Repository string

func (r Repository) Validate() error {
	if !repositoryRe.MatchString(string(r)) {
		return errors.New("must be owner/name: an owner of 1–39 letters, digits or hyphens, not starting with a hyphen, and a name of 1–100 letters, digits, dots, underscores or hyphens")
	}
	if repositoryForbiddenRe.MatchString(string(r)) {
		return errors.New(`must not end in .git (in any case), and the name must not be "." or ".."`)
	}
	return nil
}

func (Repository) JSONSchema() Schema {
	return Schema{"type": "string", "pattern": repositoryRe.String(), "not": Schema{"pattern": repositoryForbiddenRe.String()}}
}

// Branch is a branch name, as git check-ref-format allows it, narrowed to
// letters, digits, dots, underscores, hyphens and slashes. It only reaches
// git as part of "refs/heads/<branch>", never a shell.
type Branch string

func (b Branch) Validate() error {
	if !branchRe.MatchString(string(b)) {
		return errors.New("must be 1–200 letters, digits, dots, underscores, hyphens or slashes")
	}
	if branchForbiddenRe.MatchString(string(b)) {
		return errors.New(`must not start with - or /, end with /, . or .lock, contain .. or //, or have a part that starts with . or ends with .lock`)
	}
	return nil
}

func (Branch) JSONSchema() Schema {
	return Schema{"type": "string", "pattern": branchRe.String(), "not": Schema{"pattern": branchForbiddenRe.String()}}
}

// CommitSHA is a full commit hash, in lowercase hex.
type CommitSHA string

func (c CommitSHA) Validate() error {
	if !commitSHARe.MatchString(string(c)) {
		return errors.New("must be 40 lowercase hex digits")
	}
	return nil
}

func (CommitSHA) JSONSchema() Schema {
	return Schema{"type": "string", "pattern": commitSHARe.String()}
}

// ReleaseID names a release: <UTC yyyymmddhhmmss>-<first 7 hex of the
// commit>. The agent picks it; the panel only sends one back (site.rollback).
type ReleaseID string

func (r ReleaseID) Validate() error {
	if !releaseIDRe.MatchString(string(r)) {
		return errors.New("must be a release id: 14 digits, a hyphen and 7 lowercase hex digits")
	}
	return nil
}

func (ReleaseID) JSONSchema() Schema {
	return Schema{"type": "string", "pattern": releaseIDRe.String()}
}

// MaxTextFileBytes caps a deploy script and a .env (R02-D2, R02-D10).
const MaxTextFileBytes = 64 << 10

// checkTextFile: UTF-8, no NUL, at most 64 KiB.
func checkTextFile(s string) error {
	switch {
	case len(s) > MaxTextFileBytes:
		return errors.New("must be at most 65536 bytes")
	case !utf8.ValidString(s):
		return errors.New("must be valid UTF-8")
	case strings.IndexByte(s, 0) >= 0:
		return errors.New("must not contain a NUL character")
	}
	return nil
}

// textFileSchema states checkTextFile, except that JSON Schema counts
// characters, not bytes: a value of multi-byte characters can pass the
// schema's maxLength and still be over 64 KiB. Only Validate enforces the
// byte cap.
func textFileSchema() Schema {
	return Schema{"type": "string", "maxLength": MaxTextFileBytes, "pattern": noNULRe.String()}
}

// DeployScript is the owner's deploy script: shell text that only the site
// user's bash runs, from stdin (R02-D2). It is audited as it is; it is not
// a secret.
type DeployScript string

func (s DeployScript) Validate() error { return checkTextFile(string(s)) }

func (DeployScript) JSONSchema() Schema { return textFileSchema() }

// EnvContent is a site's shared .env. Fields of this type are tagged
// secret:"true" (R02-D10).
type EnvContent string

func (c EnvContent) Validate() error { return checkTextFile(string(c)) }

func (EnvContent) JSONSchema() Schema { return textFileSchema() }

// DeployTrigger is what started a deploy.
type DeployTrigger string

const (
	DeployManual  DeployTrigger = "manual"
	DeployWebhook DeployTrigger = "webhook"
	DeployURL     DeployTrigger = "url"
)

var deployTriggers = []DeployTrigger{DeployManual, DeployWebhook, DeployURL}

func (t DeployTrigger) Validate() error {
	if !slices.Contains(deployTriggers, t) {
		return errors.New("must be manual, webhook or url")
	}
	return nil
}

func (DeployTrigger) JSONSchema() Schema { return Schema{"type": "string", "enum": deployTriggers} }

// SiteRepoParams is site.repo's params: the repository and branch a site
// deploys from. The site's deploy key is kept unless RotateKey is set
// (R02-D4).
type SiteRepoParams struct {
	Name       SiteName   `json:"name"`
	Repository Repository `json:"repository"`
	Branch     Branch     `json:"branch"`
	RotateKey  bool       `json:"rotate_key,omitempty"`
}

func (p *SiteRepoParams) Validate() error {
	f := FieldErrors{}
	f.Check("name", p.Name)
	f.Check("repository", p.Repository)
	f.Check("branch", p.Branch)
	return f.Err()
}

// SiteRepoResult is the deploy key's public half and its SHA-256
// fingerprint ("SHA256:…"), for the owner to add to the repository. The
// private half never leaves the site's home (R02-D4).
type SiteRepoResult struct {
	Name        SiteName   `json:"name"`
	Repository  Repository `json:"repository"`
	Branch      Branch     `json:"branch"`
	PublicKey   string     `json:"public_key"`
	Fingerprint string     `json:"fingerprint"`
}

// KeepReleases is how many releases a site keeps: 1–20 (OQ5).
type KeepReleases int

const (
	minKeepReleases = 1
	maxKeepReleases = 20
	// DefaultKeepReleases is what a site keeps until a deploy says otherwise.
	DefaultKeepReleases KeepReleases = 5
)

func (k KeepReleases) Validate() error {
	if k < minKeepReleases || k > maxKeepReleases {
		return errors.New("must be between 1 and 20")
	}
	return nil
}

func (KeepReleases) JSONSchema() Schema {
	return Schema{"type": "integer", "minimum": minKeepReleases, "maximum": maxKeepReleases}
}

// SiteDeployParams is site.deploy's params (R02-D5). Without a commit the
// deploy takes the branch's head; a given commit must be on the branch.
// Without keep_releases the site keeps its stored value; with it, the agent
// stores the new value and prunes by it.
type SiteDeployParams struct {
	Name         SiteName      `json:"name"`
	Commit       *CommitSHA    `json:"commit,omitempty"`
	Script       DeployScript  `json:"script,omitempty"`
	Composer     bool          `json:"composer"`
	Trigger      DeployTrigger `json:"trigger"`
	KeepReleases *KeepReleases `json:"keep_releases,omitempty"`
}

func (p *SiteDeployParams) Validate() error {
	f := FieldErrors{}
	f.Check("name", p.Name)
	if p.Commit != nil {
		f.Check("commit", *p.Commit)
	}
	f.Check("script", p.Script)
	f.Check("trigger", p.Trigger)
	if p.KeepReleases != nil {
		f.Check("keep_releases", *p.KeepReleases)
	}
	return f.Err()
}

// SiteDeployResult is the release a deploy made live and its commit, and the
// release that was live before it. PreviousRelease is left out when that was
// no deployed release, such as a provisioned site's "initial".
type SiteDeployResult struct {
	Name            SiteName   `json:"name"`
	Release         ReleaseID  `json:"release"`
	Commit          CommitSHA  `json:"commit"`
	PreviousRelease *ReleaseID `json:"previous_release,omitempty"`
}

// SiteRollbackParams is site.rollback's params: a kept release to make live
// again.
type SiteRollbackParams struct {
	Name    SiteName  `json:"name"`
	Release ReleaseID `json:"release"`
}

func (p *SiteRollbackParams) Validate() error {
	f := FieldErrors{}
	f.Check("name", p.Name)
	f.Check("release", p.Release)
	return f.Err()
}

// SiteRollbackResult is the release now live.
type SiteRollbackResult struct {
	Name    SiteName  `json:"name"`
	Release ReleaseID `json:"release"`
}

// Release is one kept release; Active marks the live one.
type Release struct {
	ID      ReleaseID `json:"id"`
	Commit  CommitSHA `json:"commit"`
	Created time.Time `json:"created"`
	Active  bool      `json:"active"`
}

// SiteReleasesResult is a site's kept releases, newest first.
type SiteReleasesResult struct {
	Name     SiteName  `json:"name"`
	Releases []Release `json:"releases"`
}

// SiteEnvReadResult is a site's shared .env (R02-D10).
type SiteEnvReadResult struct {
	Content EnvContent `json:"content" secret:"true"`
}

// SiteEnvWriteParams is site.env.write's params: the whole new .env.
type SiteEnvWriteParams struct {
	Name    SiteName   `json:"name"`
	Content EnvContent `json:"content" secret:"true"`
}

func (p *SiteEnvWriteParams) Validate() error {
	f := FieldErrors{}
	f.Check("name", p.Name)
	f.Check("content", p.Content)
	return f.Err()
}
