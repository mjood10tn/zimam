package contract

import (
	"errors"
	"slices"
)

func init() {
	Register(Descriptor{
		Name:    "site.provision",
		Kind:    KindJob,
		Audited: true,
		Params:  func() Params { return &SiteProvisionParams{} },
		Result:  SiteProvisionResult{},
		Subject: func(p Params) string { return "site:" + string(p.(*SiteProvisionParams).Name) },
	})
	Register(Descriptor{
		Name:    "site.php",
		Kind:    KindJob,
		Audited: true,
		Params:  func() Params { return &SitePHPParams{} },
		Result:  SitePHPResult{},
		Subject: func(p Params) string { return "site:" + string(p.(*SitePHPParams).Name) },
	})
}

// SiteType is what a site serves (§9.2).
type SiteType string

const (
	SiteStatic SiteType = "static"
	SitePHP    SiteType = "php"
)

var siteTypes = []SiteType{SiteStatic, SitePHP}

func (t SiteType) Validate() error {
	if !slices.Contains(siteTypes, t) {
		return errors.New("must be static or php")
	}
	return nil
}

func (SiteType) JSONSchema() Schema {
	return Schema{"type": "string", "enum": siteTypes}
}

// SiteProvisionParams is site.provision's params (§5.3). A PHP site needs a
// PHP version; a static site has none, so the key is left out.
type SiteProvisionParams struct {
	Name       SiteName   `json:"name"`
	Domain     Domain     `json:"domain"`
	Type       SiteType   `json:"type"`
	PHPVersion PHPVersion `json:"php_version,omitempty"`
}

func (p *SiteProvisionParams) Validate() error {
	f := FieldErrors{}
	f.Check("name", p.Name)
	f.Check("domain", p.Domain)
	f.Check("type", p.Type)
	switch {
	case p.Type == SitePHP && p.PHPVersion == "":
		f["php_version"] = "is required for a PHP site"
	case p.Type == SitePHP:
		f.Check("php_version", p.PHPVersion)
	case p.PHPVersion != "":
		f["php_version"] = "must be left out for a static site"
	}
	return f.Err()
}

// SiteProvisionResult is what a provisioned site reports: the template set
// its files were rendered from (§5.5).
type SiteProvisionResult struct {
	Name            SiteName `json:"name"`
	TemplateVersion int      `json:"template_version"`
}

// SitePHPParams is site.php's params (§5.3): the site and the PHP version
// it moves to.
type SitePHPParams struct {
	Name       SiteName   `json:"name"`
	PHPVersion PHPVersion `json:"php_version"`
}

func (p *SitePHPParams) Validate() error {
	f := FieldErrors{}
	f.Check("name", p.Name)
	f.Check("php_version", p.PHPVersion)
	return f.Err()
}

// SitePHPResult is what a switched site reports: the version it now runs.
type SitePHPResult struct {
	Name       SiteName   `json:"name"`
	PHPVersion PHPVersion `json:"php_version"`
}
