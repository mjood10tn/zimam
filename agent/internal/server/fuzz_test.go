package server

import (
	"context"
	"slices"
	"testing"

	"zimam/agent/contract"
	"zimam/agent/internal/audit"
	"zimam/agent/internal/commands"
)

// knownCodes are the §5.2 error codes.
var knownCodes = []contract.ErrorCode{
	contract.ErrInvalidRequest, contract.ErrUnsupportedProtocol, contract.ErrUnknownCommand,
	contract.ErrValidationFailed, contract.ErrForbidden, contract.ErrBusy, contract.ErrNotFound,
	contract.ErrConflict, contract.ErrJobFailed, contract.ErrInternal,
}

type discardAudit struct{}

func (discardAudit) Write(audit.Entry) error { return nil }

// FuzzServeLine sends arbitrary bytes on one connection. The server must not
// panic, and must answer with exactly one well-formed error line: every
// handler here fails with conflict, so even a valid request gets an error.
func FuzzServeLine(f *testing.F) {
	for _, seed := range [][]byte{
		request("agent.version", "{}"),
		request("test.read", `{"name":"shop"}`),
		request("test.read", `{"name":"Bad Name","extra":1}`),
		[]byte(`{"v":2,"id":"` + testID + `","command":"agent.version","actor":"user:1","params":{}}` + "\n"),
		[]byte("{\n"),
		[]byte("\n"),
		[]byte(`{"v":1`),
		[]byte("\xff\xfe\n"),
		{},
	} {
		f.Add(seed)
	}
	srv := startServer(f, func(s *Server) {
		s.Audit = discardAudit{}
		s.Handlers = commands.Handlers{}
		for _, d := range contract.All() {
			s.Handlers[d.Name] = func(context.Context, contract.Params) (any, error) {
				return nil, &contract.AgentError{Code: contract.ErrConflict, Message: "fuzz"}
			}
		}
	})
	f.Fuzz(func(t *testing.T, in []byte) {
		e := errorReply(t, exchange(t, srv.sock, in))
		if !slices.Contains(knownCodes, e.Code) || e.Message == "" {
			t.Fatalf("error line %+v has an unknown code or no message", e)
		}
	})
}
