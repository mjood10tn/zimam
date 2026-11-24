package contract

import "time"

// cloudflare.mode turns Nginx's realip for Cloudflare's ranges on or off
// (R06-D7) and, optionally, opens 80 and 443 only to those ranges (R06-D9).
// Only a change to the origin restriction is guarded.
func init() {
	Register(Descriptor{
		Name:    "cloudflare.mode",
		Kind:    KindJob,
		Audited: true,
		Params:  func() Params { return &CloudflareModeParams{} },
		Result:  CloudflareModeResult{},
		Subject: func(Params) string { return "server" },
	})
}

type CloudflareModeParams struct {
	Enabled        bool `json:"enabled"`
	RestrictOrigin bool `json:"restrict_origin"`
}

func (p *CloudflareModeParams) Validate() error {
	if p.RestrictOrigin && !p.Enabled {
		return (FieldErrors{"restrict_origin": "needs Cloudflare mode on"}).Err()
	}
	return nil
}

// CloudflareModeResult carries a change to confirm only when the origin
// restriction changed.
type CloudflareModeResult struct {
	Enabled        bool       `json:"enabled"`
	RestrictOrigin bool       `json:"restrict_origin"`
	Change         *ChangeID  `json:"change,omitempty"`
	Deadline       *time.Time `json:"deadline,omitempty"`
}
