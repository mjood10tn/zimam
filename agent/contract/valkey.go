package contract

// The cache commands (v0.3.0 plan, "New agent commands").
func init() {
	Register(Descriptor{
		Name:    "valkey.install",
		Kind:    KindJob,
		Audited: true,
		Params:  func() Params { return &ValkeyInstallParams{} },
		Result:  ServiceInstallResult{},
		Subject: func(Params) string { return "server" },
	})
	// The password is a secret: the audit log and agent_actions redact it.
	Register(Descriptor{
		Name:    "cache.user",
		Kind:    KindJob,
		Audited: true,
		Params:  func() Params { return &CacheUserParams{} },
		Result:  ServiceUserResult{},
		Subject: func(p Params) string { return "site:" + string(p.(*CacheUserParams).Site) },
	})
}

// ValkeyInstallParams is valkey.install's params: none.
type ValkeyInstallParams struct{}

func (ValkeyInstallParams) Validate() error { return nil }

// CacheUserParams is cache.user's params: the site's Valkey user and the
// password it gets, whether the user is new or rotated.
type CacheUserParams struct {
	Site     SiteName        `json:"site"`
	Password ServicePassword `json:"password" secret:"true"`
}

func (p *CacheUserParams) Validate() error {
	f := FieldErrors{}
	f.Check("site", p.Site)
	f.Check("password", p.Password)
	return f.Err()
}
