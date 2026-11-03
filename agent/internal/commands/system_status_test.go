package commands_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"zimam/agent/contract"
	"zimam/agent/internal/audit"
	"zimam/agent/internal/commands"
	"zimam/agent/internal/config"
	"zimam/agent/internal/paths"
	"zimam/agent/internal/release"
	"zimam/agent/internal/server"
	"zimam/agent/internal/sysexec"
)

var statusNow = time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)

// statusDeps is a server's files under a temporary root: agent.json says the
// default PHP is 8.4 and the panel runs on 8.5. Nothing else exists yet.
func statusDeps(t *testing.T, run sysexec.Runner) sysexec.Deps {
	t.Helper()
	p := paths.Paths{Root: t.TempDir()}
	if err := config.Save(p, config.New("panel.example.com", "8.4", "8.5")); err != nil {
		t.Fatal(err)
	}
	return sysexec.Deps{Paths: p, Run: run, Logf: t.Logf, Now: func() time.Time { return statusNow }}
}

func touch(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, nil, 0o755); err != nil {
		t.Fatal(err)
	}
}

func copyFixture(t *testing.T, name, to string) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(to, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// nginxV answers nginx -v as Ubuntu 26.04's Nginx does: on stderr.
func nginxV(t *testing.T) sysexec.Script {
	t.Helper()
	b, err := os.ReadFile("testdata/nginx-v.txt")
	if err != nil {
		t.Fatal(err)
	}
	return sysexec.Script{Path: "/usr/sbin/nginx", Args: []string{"-v"}, Result: sysexec.Result{Stderr: b}}
}

// fpmActive answers systemctl is-active for a version's FPM service: exit
// 0 when it runs, 3 when it doesn't.
func fpmActive(v string, active bool) sysexec.Script {
	s := sysexec.Script{Path: "/usr/bin/systemctl", Args: []string{"is-active", "--quiet", "php" + v + "-fpm"}}
	if !active {
		s.Result.ExitCode = 3
	}
	return s
}

func getStatus(t *testing.T, d sysexec.Deps) contract.SystemStatusResult {
	t.Helper()
	return callStatus(t, statusHandler(t, d))
}

func statusHandler(t *testing.T, d sysexec.Deps) commands.Handler {
	t.Helper()
	h := commands.New(d, nil)["system.status"]
	if h == nil {
		t.Fatal("New has no system.status handler")
	}
	return h
}

func callStatus(t *testing.T, h commands.Handler) contract.SystemStatusResult {
	t.Helper()
	res, err := h(context.Background(), &contract.SystemStatusParams{})
	if err != nil {
		t.Fatalf("system.status: %v", err)
	}
	st, ok := res.(contract.SystemStatusResult)
	if !ok {
		t.Fatalf("system.status answered %T, want contract.SystemStatusResult", res)
	}
	return st
}

// available is the allowlist as system.status reports it on statusNow,
// when every version is still supported.
var available = []contract.AvailablePHP{
	{Version: "8.2", SecurityEnd: "2026-12-31", Supported: true},
	{Version: "8.3", SecurityEnd: "2027-12-31", Supported: true},
	{Version: "8.4", SecurityEnd: "2028-12-31", Supported: true},
	{Version: "8.5", SecurityEnd: "2029-12-31", Supported: true},
}

func TestStatusAllFields(t *testing.T) {
	run := sysexec.NewFakeRunner(nginxV(t), fpmActive("8.4", true), fpmActive("8.5", false))
	d := statusDeps(t, run)
	touch(t, d.Paths.PHPFPMBinary("8.4"))
	touch(t, d.Paths.PHPFPMBinary("8.5"))
	touch(t, d.Paths.RebootRequired())
	copyFixture(t, "updates.json", d.Paths.UpdatesCache())
	checked := time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)
	want := contract.SystemStatusResult{
		AgentVersion: release.Version,
		Protocols:    []int{1},
		NginxVersion: "1.28.3",
		PHP: contract.PHPStatus{
			Installed: []contract.InstalledPHP{{Version: "8.4", FPMActive: true}, {Version: "8.5", FPMActive: false}},
			Default:   "8.4",
			Panel:     "8.5",
			Available: available,
		},
		RebootRequired: true,
		Updates:        contract.UpdateCounts{Total: new(5), Security: new(2), CheckedAt: &checked},
		PanelDomain:    "panel.example.com",
		ACMEDirectory:  config.DefaultACMEDirectory,
	}
	if got := getStatus(t, d); !reflect.DeepEqual(got, want) {
		t.Errorf("system.status =\n%+v\nwant\n%+v", got, want)
	}
}

// A version is in use while it has a Zimam pool, a site's or the panel's:
// what php.uninstall refuses on.
func TestStatusPHPInUse(t *testing.T) {
	run := sysexec.NewFakeRunner(nginxV(t), fpmActive("8.3", false), fpmActive("8.4", true), fpmActive("8.5", true))
	d := statusDeps(t, run)
	for _, v := range []contract.PHPVersion{"8.3", "8.4", "8.5"} {
		touch(t, d.Paths.PHPFPMBinary(v))
	}
	touch(t, d.Paths.PHPGlobalConf("8.3")) // not a pool
	touch(t, d.Paths.PHPPool("8.4", "shop"))
	touch(t, d.Paths.PanelPool("8.5"))
	want := []contract.InstalledPHP{
		{Version: "8.3", FPMActive: false, InUse: false},
		{Version: "8.4", FPMActive: true, InUse: true},
		{Version: "8.5", FPMActive: true, InUse: true},
	}
	if got := getStatus(t, d).PHP.Installed; !reflect.DeepEqual(got, want) {
		t.Errorf("installed = %+v, want %+v", got, want)
	}
}

// Before maintenance first counts the updates there is no updates.json, and
// the counts are null. Slices are empty, never null: the schema says array.
func TestStatusBeforeFirstUpdateCheck(t *testing.T) {
	for name, setup := range map[string]func(d sysexec.Deps){
		"no updates.json": func(sysexec.Deps) {},
		"corrupt updates.json": func(d sysexec.Deps) {
			if err := os.MkdirAll(d.Paths.StateDir(), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(d.Paths.UpdatesCache(), []byte(`{"total":`), 0o600); err != nil {
				t.Fatal(err)
			}
		},
	} {
		d := statusDeps(t, sysexec.NewFakeRunner(nginxV(t)))
		setup(d)
		st := getStatus(t, d)
		if st.Updates.Total != nil || st.Updates.Security != nil || st.Updates.CheckedAt != nil {
			t.Errorf("%s: updates = %+v, want all null", name, st.Updates)
		}
		b, err := json.Marshal(st)
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{
			`"updates":{"total":null,"security":null,"checked_at":null}`,
			`"installed":[]`,
			`"protocols":[1]`,
		} {
			if !strings.Contains(string(b), want) {
				t.Errorf("%s: the result lacks %s:\n%s", name, want, b)
			}
		}
	}
}

func TestStatusRebootRequired(t *testing.T) {
	d := statusDeps(t, sysexec.NewFakeRunner(nginxV(t)))
	if getStatus(t, d).RebootRequired {
		t.Error("reboot_required is true without /run/reboot-required")
	}
	touch(t, d.Paths.RebootRequired())
	if !getStatus(t, d).RebootRequired {
		t.Error("reboot_required is false with /run/reboot-required")
	}
}

// A version is supported through its last day of security support (§8.1).
func TestStatusPHPSupportedFlag(t *testing.T) {
	for _, tc := range []struct {
		now     time.Time
		support bool // 8.2's, which ends on 2026-12-31
	}{
		{time.Date(2026, 12, 31, 23, 59, 59, 0, time.UTC), true},
		{time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC), false},
	} {
		d := statusDeps(t, sysexec.NewFakeRunner(nginxV(t)))
		d.Now = func() time.Time { return tc.now }
		got := getStatus(t, d).PHP.Available
		want := append([]contract.AvailablePHP(nil), available...)
		want[0].Supported = tc.support
		if !reflect.DeepEqual(got, want) {
			t.Errorf("on %v: available = %+v, want %+v", tc.now, got, want)
		}
	}
}

// serve runs for weeks, while php.default and logs.policy jobs change
// agent.json, so system.status reads it on every call.
func TestStatusReadsConfigEachCall(t *testing.T) {
	d := statusDeps(t, sysexec.NewFakeRunner(nginxV(t)))
	h := statusHandler(t, d) // one handler, as serve installs it once
	if got := callStatus(t, h).PHP.Default; got != "8.4" {
		t.Fatalf("default PHP = %s, want 8.4", got)
	}
	cfg, err := config.Load(d.Paths)
	if err != nil {
		t.Fatal(err)
	}
	cfg.DefaultPHP = "8.3"
	if err := config.Save(d.Paths, cfg); err != nil {
		t.Fatal(err)
	}
	if got := callStatus(t, h).PHP.Default; got != "8.3" {
		t.Errorf("after agent.json changed, default PHP = %s, want 8.3", got)
	}
}

// systemctl answering "inactive" is a normal answer; systemctl failing to
// run at all, or the request's deadline passing, fails the call.
func TestStatusFPMCheckFails(t *testing.T) {
	broken := fpmActive("8.4", true)
	broken.Err = errors.New("fork/exec /usr/bin/systemctl: no such file or directory")
	d := statusDeps(t, sysexec.NewFakeRunner(nginxV(t), broken))
	touch(t, d.Paths.PHPFPMBinary("8.4"))
	if _, err := statusHandler(t, d)(context.Background(), &contract.SystemStatusParams{}); err == nil || !strings.Contains(err.Error(), "no such file") {
		t.Errorf("system.status = %v, want systemctl's error", err)
	}
}

// One tool that fails doesn't blank the dashboard: the Nginx version is
// reported as unknown and the rest still answers.
func TestStatusNginxVersionUnknown(t *testing.T) {
	failing := sysexec.Script{Path: "/usr/sbin/nginx", Args: []string{"-v"}, Result: sysexec.Result{ExitCode: 1}}
	d := statusDeps(t, sysexec.NewFakeRunner(failing))
	st := getStatus(t, d)
	if st.NginxVersion != "" {
		t.Errorf("nginx_version = %q, want empty when nginx -v fails", st.NginxVersion)
	}
	if st.PanelDomain != "panel.example.com" {
		t.Errorf("panel_domain = %q, want the rest of the status", st.PanelDomain)
	}
}

// system.status is a routine read: answering it writes nothing to the audit
// log (§5.8). A rejected system.status, as the control, is audited.
func TestStatusNotAudited(t *testing.T) {
	d := statusDeps(t, sysexec.NewFakeRunner(nginxV(t)))
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
		return `{"v":1,"id":"01JBX7Q2M5YQ8T3N6V0R4K9W1E","command":"system.status","actor":"user:1","params":` + params + "}\n"
	}

	reply := exchange(t, sock, request("{}"))
	if !strings.HasPrefix(reply, `{"type":"result","ok":true,`) {
		t.Fatalf("system.status answered %s", reply)
	}
	if b, err := os.ReadFile(d.Paths.AuditLog()); err == nil && len(b) > 0 {
		t.Errorf("answering system.status wrote to the audit log:\n%s", b)
	}

	reply = exchange(t, sock, request(`{"extra":1}`))
	if !strings.Contains(reply, `"code":"validation_failed"`) {
		t.Fatalf("system.status with an unknown field answered %s", reply)
	}
	b, err := os.ReadFile(d.Paths.AuditLog())
	if err != nil {
		t.Fatalf("the rejected request wasn't audited: %v", err)
	}
	if lines := strings.Split(strings.TrimSpace(string(b)), "\n"); len(lines) != 1 || !strings.Contains(lines[0], `"outcome":"error:validation_failed"`) {
		t.Errorf("audit log =\n%s\nwant one error:validation_failed entry", b)
	}
}

// exchange sends one request line and returns the whole reply.
func exchange(t *testing.T, sock, line string) string {
	t.Helper()
	c, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := io.WriteString(c, line); err != nil {
		t.Fatal(err)
	}
	c.(*net.UnixConn).CloseWrite()
	var out bytes.Buffer
	if _, err := io.Copy(&out, c); err != nil {
		t.Fatal(err)
	}
	return out.String()
}
