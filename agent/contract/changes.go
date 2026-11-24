package contract

import "errors"

// change.confirm keeps a guarded change: it stops the change's revert timer
// before its deadline (R06-D1, D2). It answers directly, like job.cancel,
// because the countdown can't wait in the job queue.
func init() {
	Register(Descriptor{
		Name:    "change.confirm",
		Kind:    KindDirect,
		Audited: true,
		Params:  func() Params { return &ChangeConfirmParams{} },
		Result:  ChangeConfirmResult{},
	})
}

// ChangeID names a guarded change: its record in /var/lib/zimam/changes and
// its zimam-revert-<id> timer (R06-D1).
type ChangeID string

func (c ChangeID) Validate() error {
	if !ulidRe.MatchString(string(c)) {
		return errors.New("must be a ULID")
	}
	return nil
}

func (ChangeID) JSONSchema() Schema {
	return Schema{"type": "string", "pattern": ulidRe.String()}
}

type ChangeConfirmParams struct {
	Change ChangeID `json:"change"`
}

func (p *ChangeConfirmParams) Validate() error {
	f := FieldErrors{}
	f.Check("change", p.Change)
	return f.Err()
}

// ChangeConfirmResult is the change that is now kept.
type ChangeConfirmResult struct {
	Change ChangeID `json:"change"`
}
