package commands

import (
	"context"
	"errors"
	"log"
	"time"

	"zimam/agent/contract"
	"zimam/agent/internal/audit"
	"zimam/agent/internal/changes"
	"zimam/agent/internal/sysexec"
)

// changeConfirm keeps a guarded change before its revert timer fires
// (R06-D1, D2). It answers directly: the countdown can't wait in the job
// queue.
func changeConfirm(d sysexec.Deps) Handler {
	return func(ctx context.Context, p contract.Params) (any, error) {
		id := p.(*contract.ChangeConfirmParams).Change
		start := time.Now()
		err := changes.Confirm(ctx, d, id)
		auditChange(ctx, d, "change.confirm", p, start, err)
		if err != nil {
			return nil, err
		}
		return contract.ChangeConfirmResult{Change: id}, nil
	}
}

// auditChange writes the entry of a change.confirm or change.revert, which
// are direct commands: like job.cancel, the handler audits every outcome,
// with the caller (global rule 7).
func auditChange(ctx context.Context, d sysexec.Deps, command string, p contract.Params, start time.Time, err error) {
	c := audit.CallerFrom(ctx)
	e := audit.Entry{
		TS: start.UTC(), PeerUID: c.PeerUID, Actor: c.Actor, RequestID: c.RequestID,
		Command: command, Params: audit.Redact(p), Outcome: "ok", DurationMS: time.Since(start).Milliseconds(),
	}
	if err != nil {
		code := contract.ErrInternal
		var ae *contract.AgentError
		if errors.As(err, &ae) {
			code = ae.Code
		}
		e.Outcome = "error:" + string(code)
	}
	if werr := (audit.Writer{Paths: d.Paths}).Write(e); werr != nil {
		log.Printf("audit: %v", werr)
	}
}
