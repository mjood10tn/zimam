package commands_test

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"zimam/agent/internal/audit"
	"zimam/agent/internal/commands"
	"zimam/agent/internal/server"
	"zimam/agent/internal/sysexec"
)

// logs.list and logs.read are routine reads, asked every 3 s while a log
// is followed: answering them writes nothing to the audit log (§5.8). A
// rejected one, as the control, is audited.
func TestLogsNotAudited(t *testing.T) {
	d := statusDeps(t, sysexec.NewFakeRunner())
	for _, dir := range []string{d.Paths.LogDir(), d.Paths.LocksDir()} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	sock := filepath.Join(t.TempDir(), "agent.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	s := &server.Server{Listener: ln, Allowed: func(uint32) bool { return true }, Audit: audit.Writer{Paths: d.Paths}, Handlers: commands.New(d, nil), RequestTimeout: 5 * time.Second, DirectTimeout: 5 * time.Second}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Serve(ctx) }()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Serve: %v", err)
		}
	}()
	request := func(command, params string) string {
		return `{"v":1,"id":"01JBX7Q2M5YQ8T3N6V0R4K9W1E","command":"` + command + `","actor":"user:1","params":` + params + "}\n"
	}
	for _, r := range []string{
		request("logs.list", `{"source":"file.zimam-audit"}`),
		request("logs.read", `{"source":"file.nginx-error","direction":"back"}`),
	} {
		if reply := exchange(t, sock, r); !strings.Contains(reply, `"type":"result"`) && !strings.Contains(reply, `"type":"error"`) {
			t.Fatalf("reply %s", reply)
		}
	}
	if b, err := os.ReadFile(d.Paths.AuditLog()); err == nil && len(b) > 0 {
		t.Errorf("answering logs.* wrote to the audit log:\n%s", b)
	}
	if reply := exchange(t, sock, request("logs.read", `{"source":"file.zimam-audit","direction":"back","archive":"../../etc/shadow"}`)); !strings.Contains(reply, `"code":"validation_failed"`) {
		t.Fatalf("a traversal archive answered %s", reply)
	}
	b, err := os.ReadFile(d.Paths.AuditLog())
	if err != nil || strings.Count(strings.TrimSpace(string(b)), "\n") != 0 || !strings.Contains(string(b), `"outcome":"error:validation_failed"`) {
		t.Errorf("audit log = %s, %v; want one rejection", b, err)
	}
}
