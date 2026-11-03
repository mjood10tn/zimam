package contract

func init() {
	Register(Descriptor{
		Name:    "php.install",
		Kind:    KindJob,
		Audited: true,
		Params:  func() Params { return &PHPVersionParams{} },
		Result:  PHPVersionResult{},
		Subject: phpSubject,
	})
	Register(Descriptor{
		Name:    "php.uninstall",
		Kind:    KindJob,
		Audited: true,
		Params:  func() Params { return &PHPVersionParams{} },
		Result:  PHPVersionResult{},
		Subject: phpSubject,
	})
	// php.default changes agent.json, as logs.policy does, so both run under
	// the server subject.
	Register(Descriptor{
		Name:    "php.default",
		Kind:    KindJob,
		Audited: true,
		Params:  func() Params { return &PHPVersionParams{} },
		Result:  PHPVersionResult{},
		Subject: func(Params) string { return "server" },
	})
}

func phpSubject(p Params) string { return "php:" + string(p.(*PHPVersionParams).Version) }

// PHPVersionParams is the params of php.install, php.uninstall and
// php.default (§5.3).
type PHPVersionParams struct {
	Version PHPVersion `json:"version"`
}

func (p *PHPVersionParams) Validate() error {
	f := FieldErrors{}
	f.Check("version", p.Version)
	return f.Err()
}

// PHPVersionResult is what the PHP commands answer: the version they
// installed, uninstalled or made the default.
type PHPVersionResult struct {
	Version PHPVersion `json:"version"`
}
