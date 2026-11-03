package contract

func init() {
	Register(Descriptor{
		Name:    "site.security",
		Kind:    KindJob,
		Audited: true,
		Params:  func() Params { return &SiteSecurityParams{} },
		Result:  SiteSecurityResult{},
		Subject: func(p Params) string { return "site:" + string(p.(*SiteSecurityParams).Name) },
	})
}

// SiteSecurityParams is site.security's params (§5.3, §9.6): the whole set
// of a site's security settings, applied together.
type SiteSecurityParams struct {
	Name                 SiteName           `json:"name"`
	ForceHTTPS           bool               `json:"force_https"`
	HSTS                 HSTS               `json:"hsts"`
	HeadersPreset        bool               `json:"headers_preset"`
	BlockRiskyFunctions  bool               `json:"block_risky_functions"`
	RestrictToSiteFolder bool               `json:"restrict_to_site_folder"`
	IPAllow              IPList             `json:"ip_allow"`
	IPDeny               IPList             `json:"ip_deny"`
	PasswordProtection   PasswordProtection `json:"password_protection"`
}

// HSTS is the Strict-Transport-Security header: off when MaxAge is null.
// Preload isn't offered, because it's hard to undo (§9.6).
type HSTS struct {
	MaxAge            *HSTSMaxAge `json:"max_age"`
	IncludeSubdomains bool        `json:"include_subdomains"`
}

// PasswordProtection is HTTP basic authentication in front of the site.
// The panel hashes the passwords; plaintext never reaches the agent (§5.4).
type PasswordProtection struct {
	Enabled bool           `json:"enabled"`
	Users   BasicAuthUsers `json:"users"`
}

func (p *SiteSecurityParams) Validate() error {
	f := FieldErrors{}
	f.Check("name", p.Name)
	if p.HSTS.MaxAge != nil {
		f.Check("hsts.max_age", *p.HSTS.MaxAge)
	}
	if p.IPAllow == nil {
		f["ip_allow"] = "is required (an empty list for none)"
	} else {
		f.Check("ip_allow", p.IPAllow)
	}
	if p.IPDeny == nil {
		f["ip_deny"] = "is required (an empty list for none)"
	} else {
		f.Check("ip_deny", p.IPDeny)
	}
	switch {
	case p.PasswordProtection.Users == nil:
		f["password_protection.users"] = "is required (an empty list for none)"
	case p.PasswordProtection.Enabled && len(p.PasswordProtection.Users) == 0:
		f["password_protection.users"] = "needs at least one user while password protection is on"
	default:
		f.Check("password_protection.users", p.PasswordProtection.Users)
	}
	return f.Err()
}

// SiteSecurityResult is what site.security answers: the site whose
// settings now apply.
type SiteSecurityResult struct {
	Name SiteName `json:"name"`
}
