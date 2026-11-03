package contract

func init() {
	Register(Descriptor{
		Name:    "logs.policy",
		Kind:    KindJob,
		Audited: true,
		Params:  func() Params { return &LogsPolicyParams{} },
		Result:  LogsPolicyResult{},
		Subject: func(Params) string { return "server" },
	})
}

// LogsPolicyParams is logs.policy's params (§5.3, §11.4).
type LogsPolicyParams struct {
	RetentionDays      RetentionDays      `json:"retention_days"`
	AuditRetentionDays AuditRetentionDays `json:"audit_retention_days"` // D24
	SiteCapBytes       SiteCapBytes       `json:"site_cap_bytes"`
}

func (p *LogsPolicyParams) Validate() error {
	f := FieldErrors{}
	f.Check("retention_days", p.RetentionDays)
	f.Check("audit_retention_days", p.AuditRetentionDays)
	f.Check("site_cap_bytes", p.SiteCapBytes)
	return f.Err()
}

// LogsPolicyResult is the policy now in agent.json.
type LogsPolicyResult LogsPolicyParams
