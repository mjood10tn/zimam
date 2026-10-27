package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"zimam/agent/contract"
	"zimam/agent/internal/audit"
	"zimam/agent/internal/commands"
	"zimam/agent/internal/jobs"
	"zimam/agent/internal/paths"
	"zimam/agent/internal/release"
)

// Commands that exist only in this test binary: test.read answers with its
// params, test.block never answers, and test.e2e is E2EOnly.
func init() {
	for _, d := range []contract.Descriptor{
		{Name: "test.read"},
		{Name: "test.block"},
		{Name: "test.e2e", E2EOnly: true},
	} {
		d.Kind = contract.KindRead
		d.Params = func() contract.Params { return &testParams{} }
		d.Result = testResult{}
		contract.Register(d)
	}
}

type testParams struct {
	Name     contract.SiteName `json:"name"`
	Password string            `json:"password,omitempty" secret:"true"`
}

func (p testParams) Validate() error {
	f := contract.FieldErrors{}
	f.Check("name", p.Name)
	return f.Err()
}

type testResult struct {
	Name contract.SiteName `json:"name"`
}

const testID = "01JBX7Q2M5YQ8T3N6V0R4K9W1E"

// request is a request line from user:1.
func request(command, params string) []byte {
	return []byte(`{"v":1,"id":"` + testID + `","command":"` + command + `","actor":"user:1","params":` + params + "}\n")
}

// testHandlers are serve's handlers plus the test commands'.
func testHandlers(t testing.TB) commands.Handlers {
	unblock := make(chan struct{})
	t.Cleanup(func() { close(unblock) })
	h := commands.New(nil)
	h["test.read"] = func(_ context.Context, p contract.Params) (any, error) {
		return testResult{Name: p.(*testParams).Name}, nil
	}
	h["test.block"] = func(context.Context, contract.Params) (any, error) {
		<-unblock // ignores its context, like a stuck handler
		return testResult{}, nil
	}
	h["test.e2e"] = h["test.read"]
	return h
}

type testServer struct {
	sock  string
	paths paths.Paths
}

// startServer serves a socket under a temporary root until the test ends.
// By default it allows every caller and audits to the root's audit.log.
func startServer(t testing.TB, opts ...func(*Server)) *testServer {
	t.Helper()
	root := t.TempDir()
	p := paths.Paths{Root: root}
	for _, dir := range []string{p.LogDir(), p.LocksDir()} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	sock := filepath.Join(root, "agent.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{
		Listener:       ln,
		Allowed:        func(uint32) bool { return true },
		Audit:          audit.Writer{Paths: p},
		Handlers:       testHandlers(t),
		RequestTimeout: 5 * time.Second,
		DirectTimeout:  5 * time.Second,
	}
	for _, opt := range opts {
		opt(s)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Serve(ctx) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Serve returned %v", err)
		}
	})
	return &testServer{sock: sock, paths: p}
}

// exchange sends in on a new connection, half-closes it, and returns what
// the server writes before closing its end. The server may close with input
// unread (after an early newline, or a line over the limit); Linux then
// reports ECONNRESET to the client after the reply, so a reset ends the
// reply too.
func exchange(t testing.TB, sock string, in []byte) []byte {
	t.Helper()
	c, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	uc := c.(*net.UnixConn)
	go func() {
		uc.Write(in) // fails with EPIPE once the server has closed
		uc.CloseWrite()
	}()
	return readReply(t, uc)
}

func readReply(t testing.TB, c net.Conn) []byte {
	t.Helper()
	c.SetReadDeadline(time.Now().Add(10 * time.Second))
	out, err := io.ReadAll(c)
	if err != nil && !errors.Is(err, syscall.ECONNRESET) {
		t.Fatalf("reading the reply: %v (read %q)", err, out)
	}
	return out
}

// oneLine returns out's only line, without its newline.
func oneLine(t testing.TB, out []byte) []byte {
	t.Helper()
	line, ok := bytes.CutSuffix(out, []byte("\n"))
	if !ok || bytes.Contains(line, []byte("\n")) {
		t.Fatalf("want exactly one line, got %q", out)
	}
	return line
}

func errorReply(t testing.TB, out []byte) contract.Error {
	t.Helper()
	var e contract.Error
	if err := contract.DecodeStrict(oneLine(t, out), &e); err != nil || e.Type != "error" {
		t.Fatalf("want an error line, got %q (%v)", out, err)
	}
	return e
}

func resultReply(t testing.TB, out []byte) json.RawMessage {
	t.Helper()
	var r contract.Result
	if err := contract.DecodeStrict(oneLine(t, out), &r); err != nil || r.Type != "result" || !r.OK {
		t.Fatalf("want a result line, got %q (%v)", out, err)
	}
	return r.Data
}

func expectError(t *testing.T, srv *testServer, in []byte, code contract.ErrorCode) contract.Error {
	t.Helper()
	e := errorReply(t, exchange(t, srv.sock, in))
	if e.Code != code {
		t.Fatalf("code = %s (%q), want %s", e.Code, e.Message, code)
	}
	return e
}

// auditEntries returns the audit log's entries, decoded.
func (s *testServer) auditEntries(t testing.TB) []map[string]any {
	t.Helper()
	data, err := os.ReadFile(s.paths.AuditLog())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var entries []map[string]any
	for line := range strings.Lines(string(data)) {
		var e map[string]any
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatalf("audit line %q: %v", line, err)
		}
		entries = append(entries, e)
	}
	return entries
}

func TestRejectsDisallowedUID(t *testing.T) {
	me := uint32(os.Getuid())
	srv := startServer(t, func(s *Server) {
		s.Allowed = func(uid uint32) bool { return uid != me }
	})
	if out := exchange(t, srv.sock, nil); len(out) != 0 {
		t.Fatalf("server sent %q, want nothing", out)
	}
	entries := srv.auditEntries(t)
	if len(entries) != 1 || entries[0]["outcome"] != "error:forbidden" || entries[0]["peer_uid"] != float64(me) {
		t.Fatalf("audit = %v, want one error:forbidden entry with peer_uid %d", entries, me)
	}
}

func TestAcceptsAllowedUID(t *testing.T) {
	me := uint32(os.Getuid())
	srv := startServer(t, func(s *Server) {
		s.Allowed = func(uid uint32) bool { return uid == me }
	})
	resultReply(t, exchange(t, srv.sock, request("agent.version", "{}")))
}

func TestLineOver1MiB(t *testing.T) {
	srv := startServer(t)
	in := append(bytes.Repeat([]byte("a"), maxLine+1), '\n')
	if e := expectError(t, srv, in, contract.ErrInvalidRequest); !strings.Contains(e.Message, "too long") {
		t.Errorf("message = %q, want it to say too long", e.Message)
	}
}

func TestLineOfExactly1MiB(t *testing.T) {
	srv := startServer(t)
	in := append([]byte("{"), bytes.Repeat([]byte(" "), maxLine-1)...)
	in = append(in, '\n')
	if e := expectError(t, srv, in, contract.ErrInvalidRequest); strings.Contains(e.Message, "too long") {
		t.Errorf("message = %q; a line of exactly 1 MiB isn't too long", e.Message)
	}
}

func TestRequestTimeout(t *testing.T) {
	srv := startServer(t, func(s *Server) { s.RequestTimeout = 100 * time.Millisecond })
	c, err := net.Dial("unix", srv.sock)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := c.Write([]byte(`{"v":1`)); err != nil { // no newline; the connection stays open
		t.Fatal(err)
	}
	if e := errorReply(t, readReply(t, c)); e.Code != contract.ErrInvalidRequest {
		t.Errorf("code = %s, want invalid_request", e.Code)
	}
	if entries := srv.auditEntries(t); len(entries) != 1 || entries[0]["outcome"] != "error:invalid_request" {
		t.Errorf("audit = %v, want one error:invalid_request entry", entries)
	}
}

func TestInvalidJSON(t *testing.T) {
	srv := startServer(t)
	expectError(t, srv, []byte(`{"v":1,"id":`+"\n"), contract.ErrInvalidRequest)
}

func TestMissingEnvelopeField(t *testing.T) {
	srv := startServer(t)
	for _, in := range []string{
		`{"v":1,"command":"agent.version","actor":"user:1","params":{}}`,
		`{"v":1,"id":"` + testID + `","actor":"user:1","params":{}}`,
		`{"v":1,"id":"` + testID + `","command":"agent.version","params":{}}`,
		`{"v":1,"id":"` + testID + `","command":"agent.version","actor":"","params":{}}`,
		`{"v":1,"id":"not-a-ulid","command":"agent.version","actor":"user:1","params":{}}`,
	} {
		expectError(t, srv, []byte(in+"\n"), contract.ErrInvalidRequest)
	}
}

func TestUnsupportedProtocol(t *testing.T) {
	srv := startServer(t)
	in := bytes.Replace(request("agent.version", "{}"), []byte(`"v":1`), []byte(`"v":2`), 1)
	expectError(t, srv, in, contract.ErrUnsupportedProtocol)
}

func TestUnknownCommand(t *testing.T) {
	srv := startServer(t)
	expectError(t, srv, request("nope.nothing", "{}"), contract.ErrUnknownCommand)
	// Registered, but this agent has no handler for it yet.
	expectError(t, srv, request("system.status", "{}"), contract.ErrUnknownCommand)
}

func TestUnknownTopLevelField(t *testing.T) {
	srv := startServer(t)
	in := bytes.Replace(request("agent.version", "{}"), []byte(`{"v":1`), []byte(`{"extra":true,"v":1`), 1)
	expectError(t, srv, in, contract.ErrInvalidRequest)
}

func TestUnknownParamsField(t *testing.T) {
	srv := startServer(t)
	e := expectError(t, srv, request("test.read", `{"name":"shop","extra":true}`), contract.ErrValidationFailed)
	if e.Fields["params"] == "" {
		t.Errorf("fields = %v, want fields.params set", e.Fields)
	}
}

// Keys are checked across the whole line, params included, before anything
// is decoded.
func TestUppercaseKey(t *testing.T) {
	srv := startServer(t)
	in := bytes.Replace(request("agent.version", "{}"), []byte(`"command"`), []byte(`"Command"`), 1)
	expectError(t, srv, in, contract.ErrInvalidRequest)
	expectError(t, srv, request("test.read", `{"Name":"shop"}`), contract.ErrInvalidRequest)
}

func TestDuplicateKey(t *testing.T) {
	srv := startServer(t)
	in := bytes.Replace(request("agent.version", "{}"), []byte(`"v":1`), []byte(`"v":1,"v":1`), 1)
	expectError(t, srv, in, contract.ErrInvalidRequest)
	expectError(t, srv, request("test.read", `{"name":"shop","name":"shop"}`), contract.ErrInvalidRequest)
}

func TestValidationErrorShape(t *testing.T) {
	srv := startServer(t)
	out := exchange(t, srv.sock, request("test.read", `{"name":"Bad Name"}`))
	msg, err := json.Marshal(contract.SiteName("Bad Name").Validate().Error())
	if err != nil {
		t.Fatal(err)
	}
	want := `{"type":"error","code":"validation_failed","message":"Invalid parameters","fields":{"name":` + string(msg) + "}}\n"
	if string(out) != want {
		t.Errorf("reply = %s\nwant    %s", out, want)
	}
}

func TestDirectCommandTimeout(t *testing.T) {
	srv := startServer(t, func(s *Server) { s.DirectTimeout = 100 * time.Millisecond })
	expectError(t, srv, request("test.block", `{"name":"shop"}`), contract.ErrInternal)
}

func TestOneRequestPerConnection(t *testing.T) {
	srv := startServer(t)
	in := append(request("agent.version", "{}"), request("agent.version", "{}")...)
	resultReply(t, exchange(t, srv.sock, in)) // fails on a second line
}

func TestAgentVersion(t *testing.T) {
	srv := startServer(t)
	data := resultReply(t, exchange(t, srv.sock, request("agent.version", "{}")))
	var got contract.AgentVersionResult
	if err := contract.DecodeStrict(data, &got); err != nil {
		t.Fatal(err)
	}
	want := contract.AgentVersionResult{Version: release.Version, Protocols: []int{1}, Build: release.Flavor}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("agent.version = %+v, want %+v", got, want)
	}
}

func TestReadsAreNotAudited(t *testing.T) {
	srv := startServer(t)
	resultReply(t, exchange(t, srv.sock, request("agent.version", "{}")))
	resultReply(t, exchange(t, srv.sock, request("test.read", `{"name":"shop"}`)))
	if entries := srv.auditEntries(t); len(entries) != 0 {
		t.Errorf("audit = %v, want no entries", entries)
	}
}

// A job command's handler returns contract.Accepted, which goes out as the
// accepted line.
func TestAcceptedReply(t *testing.T) {
	srv := startServer(t, func(s *Server) {
		s.Handlers["test.read"] = func(context.Context, contract.Params) (any, error) {
			return contract.Accepted{Job: testID}, nil
		}
	})
	out := exchange(t, srv.sock, request("test.read", `{"name":"shop"}`))
	if want := `{"type":"accepted","job":"` + testID + `"}` + "\n"; string(out) != want {
		t.Errorf("reply = %s, want %s", out, want)
	}
}

// job.follow's handler returns a commands.Stream. It runs after the handler,
// free of DirectTimeout, and sends its own lines.
func TestStreamReply(t *testing.T) {
	srv := startServer(t, func(s *Server) {
		s.DirectTimeout = 100 * time.Millisecond
		s.Handlers["test.read"] = func(context.Context, contract.Params) (any, error) {
			return commands.Stream(func(ctx context.Context, send func(any) error) error {
				if err := send(contract.LogLine{Type: "log", Job: testID, Line: "first"}); err != nil {
					return err
				}
				select {
				case <-time.After(300 * time.Millisecond): // past DirectTimeout
				case <-ctx.Done():
					return ctx.Err()
				}
				return send(contract.Result{Type: "result", OK: true, Data: json.RawMessage(`{}`)})
			}), nil
		}
	})
	out := exchange(t, srv.sock, request("test.read", `{"name":"shop"}`))
	want := `{"type":"log","job":"` + testID + `","offset":0,"line":"first"}` + "\n" + `{"type":"result","ok":true,"data":{}}` + "\n"
	if string(out) != want {
		t.Errorf("reply =\n%s\nwant\n%s", out, want)
	}
}

// job.follow for an unknown job fails before its first line, so the server
// sends the error as the only line.
func TestFollowUnknownJob(t *testing.T) {
	srv := startServer(t, func(s *Server) {
		maps.Copy(s.Handlers, commands.New(&jobs.Manager{Paths: paths.Paths{Root: t.TempDir()}}))
	})
	expectError(t, srv, request("job.follow", `{"job":"01JBX7Q3A1B2C3D4E5F6G7H8J9","from_offset":0}`), contract.ErrNotFound)
}

// Handlers that audit, such as job.cancel's, find the caller in their context.
func TestHandlersGetCaller(t *testing.T) {
	srv := startServer(t, func(s *Server) {
		s.Handlers["test.read"] = func(ctx context.Context, _ contract.Params) (any, error) {
			return audit.CallerFrom(ctx), nil
		}
	})
	data := resultReply(t, exchange(t, srv.sock, request("test.read", `{"name":"shop"}`)))
	var c audit.Caller
	if err := json.Unmarshal(data, &c); err != nil {
		t.Fatal(err)
	}
	if c.Actor != "user:1" || c.RequestID != testID || c.PeerUID == nil || *c.PeerUID != uint32(os.Getuid()) {
		t.Errorf("caller = %+v, want user:1, the request id and this process's uid", c)
	}
}

// An E2EOnly command exists only in e2e builds (D22).
func TestE2EOnlyCommand(t *testing.T) {
	srv := startServer(t)
	out := exchange(t, srv.sock, request("test.e2e", `{"name":"shop"}`))
	if release.Flavor == contract.BuildE2E {
		resultReply(t, out)
		return
	}
	if e := errorReply(t, out); e.Code != contract.ErrUnknownCommand {
		t.Errorf("code = %s, want unknown_command in a release build", e.Code)
	}
}

func TestRejectionsAreAudited(t *testing.T) {
	me := float64(os.Getuid())
	for _, tc := range []struct {
		name    string
		refuse  bool
		in      []byte
		outcome string
		command string // "" when the envelope didn't decode
		params  any    // the redacted params, or nil when they didn't decode
	}{
		{name: "forbidden", refuse: true, outcome: "error:forbidden"},
		{name: "line too long", in: append(bytes.Repeat([]byte("a"), maxLine+1), '\n'), outcome: "error:invalid_request"},
		{name: "no newline", in: []byte(`{"v":1`), outcome: "error:invalid_request"},
		{name: "invalid json", in: []byte("{\n"), outcome: "error:invalid_request"},
		{name: "uppercase key", in: request("test.read", `{"Name":"shop"}`), outcome: "error:invalid_request"},
		{name: "duplicate key", in: request("test.read", `{"name":"a","name":"b"}`), outcome: "error:invalid_request"},
		{name: "unknown top-level field", in: []byte(`{"v":1,"extra":1}` + "\n"), outcome: "error:invalid_request"},
		{
			name:    "missing actor",
			in:      []byte(`{"v":1,"id":"` + testID + `","command":"test.read","params":{}}` + "\n"),
			outcome: "error:invalid_request", command: "test.read",
		},
		{
			name:    "id not a ulid",
			in:      []byte(`{"v":1,"id":"x","command":"test.read","actor":"user:1","params":{}}` + "\n"),
			outcome: "error:invalid_request", command: "test.read",
		},
		{
			name:    "unsupported protocol",
			in:      []byte(`{"v":2,"id":"` + testID + `","command":"test.read","actor":"user:1","params":{}}` + "\n"),
			outcome: "error:unsupported_protocol", command: "test.read",
		},
		{name: "unknown command", in: request("nope.nothing", "{}"), outcome: "error:unknown_command", command: "nope.nothing"},
		{
			name:    "unknown params field",
			in:      request("test.read", `{"name":"shop","extra":1}`),
			outcome: "error:validation_failed", command: "test.read",
		},
		{
			name:    "invalid params",
			in:      request("test.read", `{"name":"Bad Name","password":"hunter2"}`),
			outcome: "error:validation_failed", command: "test.read",
			params: map[string]any{"name": "Bad Name", "password": "[redacted]"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := startServer(t, func(s *Server) {
				s.Allowed = func(uint32) bool { return !tc.refuse }
			})
			exchange(t, srv.sock, tc.in)
			entries := srv.auditEntries(t)
			if len(entries) != 1 {
				t.Fatalf("audit has %d entries, want 1: %v", len(entries), entries)
			}
			e := entries[0]
			if e["outcome"] != tc.outcome || e["command"] != tc.command || e["peer_uid"] != me {
				t.Errorf("entry = %v, want outcome %s, command %q, peer_uid %v", e, tc.outcome, tc.command, me)
			}
			if !reflect.DeepEqual(e["params"], tc.params) {
				t.Errorf("params = %#v, want %#v", e["params"], tc.params)
			}
			if raw, _ := os.ReadFile(srv.paths.AuditLog()); bytes.Contains(raw, []byte("hunter2")) {
				t.Errorf("the audit log holds the password: %s", raw)
			}
		})
	}
}
