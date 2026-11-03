package commands_test

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"zimam/agent/contract"
	"zimam/agent/internal/audit"
	"zimam/agent/internal/commands"
	"zimam/agent/internal/server"
	"zimam/agent/internal/sysexec"
)

// metricsDeps is a server whose /proc files hold one line each, enough
// for system.metrics to answer, with systemctl answering every unit.
func metricsDeps(t *testing.T) sysexec.Deps {
	t.Helper()
	units := []string{"nginx.service", "zimam-agent.socket", "zimam-panel-queue.service", "zimam-panel-scheduler.timer", "zimam-maintenance.timer", "certbot.timer", "ssh.socket", "ssh.service"}
	var out strings.Builder
	for i, u := range units {
		if i > 0 {
			out.WriteString("\n")
		}
		out.WriteString("Id=" + u + "\nActiveState=active\nUnitFileState=enabled\n")
	}
	run := sysexec.NewFakeRunner(sysexec.Script{
		Path:   "/usr/bin/systemctl",
		Args:   append([]string{"show", "-p", "Id,ActiveState,UnitFileState", "--"}, units...),
		Result: sysexec.Result{Stdout: []byte(out.String())},
	})
	d := statusDeps(t, run)
	for path, content := range map[string]string{
		d.Paths.ProcUptime():  "100.5 200.1\n",
		d.Paths.ProcLoadavg(): "0.10 0.20 0.30 1/100 1234\n",
		d.Paths.ProcStat():    "cpu  1 2 3 4 5 6 7 8 0 0\n",
		d.Paths.ProcMeminfo(): "MemTotal: 1000 kB\nMemAvailable: 500 kB\nSwapTotal: 0 kB\nSwapFree: 0 kB\n",
		d.Paths.ProcMounts():  "proc /proc proc rw 0 0\n",
		d.Paths.ProcNetDev():  "h1\nh2\n eth0: 10 1 0 0 0 0 0 0 20 1 0 0 0 0 0 0\n",
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return d
}

func TestMetricsHandler(t *testing.T) {
	h := commands.New(metricsDeps(t), nil)["system.metrics"]
	if h == nil {
		t.Fatal("New has no system.metrics handler")
	}
	res, err := h(context.Background(), &contract.SystemMetricsParams{})
	if err != nil {
		t.Fatal(err)
	}
	m, ok := res.(contract.SystemMetricsResult)
	if !ok {
		t.Fatalf("system.metrics answered %T", res)
	}
	if m.UptimeSeconds != 100.5 || m.CPU.Steal != 8 || m.Network.TxBytes != 20 || len(m.Services) != 7 {
		t.Errorf("system.metrics = %+v", m)
	}
}

// system.metrics is a routine read, asked every minute and every 5 s while
// the Monitoring page is open: answering it writes nothing to the audit log
// (§5.8). A rejected one, as the control, is audited.
func TestMetricsNotAudited(t *testing.T) {
	d := metricsDeps(t)
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
	s := &server.Server{
		Listener:       ln,
		Allowed:        func(uint32) bool { return true },
		Audit:          audit.Writer{Paths: d.Paths},
		Handlers:       commands.New(d, nil),
		RequestTimeout: 5 * time.Second,
		DirectTimeout:  5 * time.Second,
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Serve(ctx) }()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Serve: %v", err)
		}
	}()
	request := func(params string) string {
		return `{"v":1,"id":"01JBX7Q2M5YQ8T3N6V0R4K9W1E","command":"system.metrics","actor":"system","params":` + params + "}\n"
	}

	for range 3 {
		if reply := exchange(t, sock, request("{}")); !strings.HasPrefix(reply, `{"type":"result","ok":true,`) {
			t.Fatalf("system.metrics answered %s", reply)
		}
	}
	if b, err := os.ReadFile(d.Paths.AuditLog()); err == nil && len(b) > 0 {
		t.Errorf("answering system.metrics wrote to the audit log:\n%s", b)
	}

	if reply := exchange(t, sock, request(`{"extra":1}`)); !strings.Contains(reply, `"code":"validation_failed"`) {
		t.Fatalf("system.metrics with an unknown field answered %s", reply)
	}
	b, err := os.ReadFile(d.Paths.AuditLog())
	if err != nil {
		t.Fatalf("the rejected request wasn't audited: %v", err)
	}
	if lines := strings.Split(strings.TrimSpace(string(b)), "\n"); len(lines) != 1 || !strings.Contains(lines[0], `"outcome":"error:validation_failed"`) {
		t.Errorf("audit log =\n%s\nwant one error:validation_failed entry", b)
	}
}
