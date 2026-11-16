package server

import (
	"bytes"
	"context"
	"os"
	"reflect"
	"strings"
	"testing"

	"zimam/agent/contract"
	"zimam/agent/internal/audit"
	"zimam/agent/internal/paths"
)

// A read whose descriptor says Audited (site.env.read, R02-D10) writes one
// audit entry, with every secret in its params and result redacted.

const envSecret = "DB_PASSWORD=dummy-secret-value\n"

func init() {
	contract.Register(contract.Descriptor{
		Name: "test.direct", Kind: contract.KindDirect, Audited: true,
		Params: func() contract.Params { return &testParams{} }, Result: testResult{},
	})
}

func withEnvRead(h func(context.Context, contract.Params) (any, error)) func(*Server) {
	return func(s *Server) { s.Handlers["site.env.read"] = h }
}

func envReadOK(context.Context, contract.Params) (any, error) {
	return contract.SiteEnvReadResult{Content: envSecret}, nil
}

func (s *testServer) wantNoSecret(t *testing.T) {
	t.Helper()
	if raw, _ := os.ReadFile(s.paths.AuditLog()); bytes.Contains(raw, []byte("dummy-secret")) {
		t.Errorf("the audit log holds the .env: %s", raw)
	}
}

func TestEnvReadAuditedAndRedacted(t *testing.T) {
	srv := startServer(t, withEnvRead(envReadOK))
	data := resultReply(t, exchange(t, srv.sock, request("site.env.read", `{"name":"shop"}`)))
	if !bytes.Contains(data, []byte("dummy-secret-value")) {
		t.Errorf("result = %s, want the .env", data)
	}
	entries := srv.auditEntries(t)
	if len(entries) != 1 {
		t.Fatalf("audit has %d entries, want 1: %v", len(entries), entries)
	}
	e := entries[0]
	me := float64(os.Getuid())
	if e["command"] != "site.env.read" || e["outcome"] != "ok" || e["actor"] != "user:1" || e["request_id"] != testID || e["peer_uid"] != me {
		t.Errorf("entry = %v, want site.env.read, ok, user:1, the request id and this uid", e)
	}
	if want := map[string]any{"name": "shop"}; !reflect.DeepEqual(e["params"], want) {
		t.Errorf("params = %#v, want %#v", e["params"], want)
	}
	if want := map[string]any{"content": "[redacted]"}; !reflect.DeepEqual(e["result"], want) {
		t.Errorf("result = %#v, want %#v", e["result"], want)
	}
	srv.wantNoSecret(t)
}

// A failed audited read is audited too, with its error code.
func TestAuditedReadErrorAudited(t *testing.T) {
	for _, tc := range []struct {
		name    string
		err     error
		outcome string
	}{
		{"agent error", &contract.AgentError{Code: contract.ErrNotFound, Message: "Site shop has no .env."}, "error:not_found"},
		{"other error", os.ErrPermission, "error:internal"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := startServer(t, withEnvRead(func(context.Context, contract.Params) (any, error) { return nil, tc.err }))
			exchange(t, srv.sock, request("site.env.read", `{"name":"shop"}`))
			entries := srv.auditEntries(t)
			if len(entries) != 1 {
				t.Fatalf("audit has %d entries, want 1: %v", len(entries), entries)
			}
			if e := entries[0]; e["outcome"] != tc.outcome || e["command"] != "site.env.read" {
				t.Errorf("entry = %v, want outcome %s", e, tc.outcome)
			}
			if _, ok := entries[0]["result"]; ok {
				t.Errorf("entry = %v, want no result", entries[0])
			}
		})
	}
}

// A secret that can't be audited doesn't leave the agent (§5.8).
func TestAuditedReadWithheldWhenAuditFails(t *testing.T) {
	srv := startServer(t, withEnvRead(envReadOK), func(s *Server) {
		s.Audit = audit.Writer{Paths: paths.Paths{Root: t.TempDir()}} // no var/log/zimam
	})
	out := exchange(t, srv.sock, request("site.env.read", `{"name":"shop"}`))
	if bytes.Contains(out, []byte("dummy-secret")) {
		t.Fatalf("reply = %s, want the .env withheld", out)
	}
	if e := errorReply(t, out); e.Code != contract.ErrInternal {
		t.Errorf("code = %s, want internal", e.Code)
	}
}

// Only reads are audited by the dispatcher: a direct command (job.cancel)
// and a job's submission audit themselves, and must not be audited twice.
func TestDispatcherAuditsOnlyAuditedReads(t *testing.T) {
	srv := startServer(t, func(s *Server) {
		s.Handlers["test.direct"] = func(context.Context, contract.Params) (any, error) { return testResult{}, nil }
	})
	resultReply(t, exchange(t, srv.sock, request("test.direct", `{"name":"shop"}`)))
	resultReply(t, exchange(t, srv.sock, request("test.read", `{"name":"shop"}`)))
	if entries := srv.auditEntries(t); len(entries) != 0 {
		t.Errorf("audit = %v, want no entries", entries)
	}
}

// A refused site.env.write is audited without its content.
func TestEnvWriteRejectionAuditedWithoutContent(t *testing.T) {
	srv := startServer(t, func(s *Server) {
		s.Handlers["site.env.write"] = func(context.Context, contract.Params) (any, error) {
			t.Error("the handler ran for refused content")
			return nil, nil
		}
	})
	for _, content := range []string{`DB_PASSWORD=dummy-secret\u0000`, "DB_PASSWORD=dummy-secret" + strings.Repeat("x", contract.MaxTextFileBytes)} {
		out := exchange(t, srv.sock, request("site.env.write", `{"name":"shop","content":"`+content+`"}`))
		if e := errorReply(t, out); e.Code != contract.ErrValidationFailed || e.Fields["content"] == "" {
			t.Errorf("reply = %+v, want validation_failed on content", e)
		}
		if bytes.Contains(out, []byte("dummy-secret")) {
			t.Errorf("reply quotes the content: %s", out)
		}
	}
	entries := srv.auditEntries(t)
	if len(entries) != 2 {
		t.Fatalf("audit has %d entries, want 2: %v", len(entries), entries)
	}
	for _, e := range entries {
		want := map[string]any{"name": "shop", "content": "[redacted]"}
		if e["command"] != "site.env.write" || e["outcome"] != "error:validation_failed" || !reflect.DeepEqual(e["params"], want) {
			t.Errorf("entry = %v, want site.env.write, validation_failed, content redacted", e)
		}
	}
	srv.wantNoSecret(t)
}
