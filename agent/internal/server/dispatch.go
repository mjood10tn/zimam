package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"runtime/debug"
	"slices"
	"strings"
	"time"

	"zimam/agent/contract"
	"zimam/agent/internal/audit"
	"zimam/agent/internal/commands"
	"zimam/agent/internal/release"
)

const invalidParams = "Invalid parameters"

// call is one request, as far as it has been read. A rejection audits it.
type call struct {
	start  time.Time
	uid    *uint32 // nil when SO_PEERCRED failed
	req    contract.Request
	params contract.Params // set once the params decode
}

// dispatch answers the request line of an allowed caller and returns the
// reply. The checks run in the order of design §5.2.
func (s *Server) dispatch(ctx context.Context, cl *call, line []byte) any {
	var req contract.Request
	if err := contract.DecodeStrict(line, &req); err != nil {
		return s.reject(cl, contract.ErrInvalidRequest, "Invalid request: "+clip(err.Error()), nil)
	}
	cl.req = req
	if missing := missingFields(req); len(missing) > 0 {
		return s.reject(cl, contract.ErrInvalidRequest, "Missing envelope fields: "+strings.Join(missing, ", "), nil)
	}
	if contract.JobID(req.ID).Validate() != nil { // request ids are ULIDs, like job ids
		return s.reject(cl, contract.ErrInvalidRequest, "Request id must be a ULID", nil)
	}
	if !slices.Contains(contract.SupportedProtocols, req.V) {
		return s.reject(cl, contract.ErrUnsupportedProtocol, fmt.Sprintf("Protocol version %d is not supported", req.V), nil)
	}
	// A registered command without a handler isn't implemented by this agent
	// yet. E2EOnly commands don't exist in release builds (D22).
	d, ok := contract.Lookup(req.Command)
	h := s.Handlers[req.Command]
	if !ok || h == nil || (d.E2EOnly && release.Flavor != contract.BuildE2E) {
		return s.reject(cl, contract.ErrUnknownCommand, fmt.Sprintf("Unknown command %q", clip(req.Command)), nil)
	}
	p := d.Params()
	if err := contract.DecodeStrict(req.Params, p); err != nil {
		return s.reject(cl, contract.ErrValidationFailed, invalidParams, contract.FieldErrors{"params": clip(err.Error())})
	}
	cl.params = p
	if err := p.Validate(); err != nil {
		var ae *contract.AgentError
		if !errors.As(err, &ae) {
			ae = &contract.AgentError{Code: contract.ErrValidationFailed, Message: invalidParams, Fields: contract.FieldErrors{"params": clip(err.Error())}}
		}
		return s.reject(cl, ae.Code, ae.Message, ae.Fields)
	}
	// Handlers that audit, such as job.cancel's, need the caller.
	ctx = audit.WithCaller(ctx, audit.Caller{PeerUID: cl.uid, Actor: clip(req.Actor), RequestID: req.ID})
	v, err := s.run(ctx, h, p)
	if err != nil {
		return failure(req.Command, err)
	}
	switch v := v.(type) {
	case contract.Accepted:
		v.Type = "accepted"
		return v
	case commands.Stream:
		return v // serveConn runs it
	}
	data, err := json.Marshal(v)
	if err != nil {
		log.Printf("%s: encoding the result: %v", req.Command, err)
		return errorLine(contract.ErrInternal, "Internal error", nil)
	}
	return contract.Result{Type: "result", OK: true, Data: data}
}

// reject audits a rejected request (§5.8) and returns its error line. The
// entry is written before the reply, so a client that has the reply finds
// the entry.
func (s *Server) reject(cl *call, code contract.ErrorCode, message string, fields contract.FieldErrors) contract.Error {
	e := audit.Entry{
		TS:         cl.start.UTC(),
		PeerUID:    cl.uid,
		Actor:      clip(cl.req.Actor),
		RequestID:  clip(cl.req.ID),
		Command:    clip(cl.req.Command),
		Outcome:    "error:" + string(code),
		DurationMS: time.Since(cl.start).Milliseconds(),
	}
	if cl.params != nil {
		e.Params = audit.Redact(cl.params)
	}
	if err := s.Audit.Write(e); err != nil {
		log.Printf("audit: %v", err)
	}
	return errorLine(code, message, fields)
}

func errorLine(code contract.ErrorCode, message string, fields contract.FieldErrors) contract.Error {
	return contract.Error{Type: "error", Code: code, Message: message, Fields: fields}
}

func missingFields(r contract.Request) []string {
	var missing []string
	for _, f := range []struct{ name, value string }{{"id", r.ID}, {"command", r.Command}, {"actor", r.Actor}} {
		if f.value == "" {
			missing = append(missing, f.name)
		}
	}
	return missing
}

// run calls h under the direct-command timeout. A handler that ignores its
// context is abandoned at the deadline and finishes on its own.
func (s *Server) run(ctx context.Context, h commands.Handler, p contract.Params) (any, error) {
	ctx, cancel := context.WithTimeout(ctx, s.DirectTimeout)
	defer cancel()
	type outcome struct {
		v   any
		err error
	}
	done := make(chan outcome, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				done <- outcome{err: fmt.Errorf("panic: %v\n%s", r, debug.Stack())}
			}
		}()
		v, err := h(ctx, p)
		done <- outcome{v, err}
	}()
	select {
	case o := <-done:
		return o.v, o.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// failure turns a handler's error into the reply. Anything but an AgentError
// is internal, with the details in the journal rather than the reply.
func failure(command string, err error) contract.Error {
	var ae *contract.AgentError
	if errors.As(err, &ae) {
		return errorLine(ae.Code, ae.Message, ae.Fields)
	}
	log.Printf("%s: %v", command, err)
	if errors.Is(err, context.DeadlineExceeded) {
		return errorLine(contract.ErrInternal, "The command did not finish in time", nil)
	}
	return errorLine(contract.ErrInternal, "Internal error", nil)
}

// clip shortens a client's string for a reply or an audit entry: a request
// line can be 1 MiB, and these strings aren't validated.
func clip(s string) string {
	const limit = 200
	if len(s) <= limit {
		return s
	}
	return strings.ToValidUTF8(s[:limit], "") + "…"
}
