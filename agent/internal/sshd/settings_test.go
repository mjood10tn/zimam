package sshd

import (
	"context"
	"encoding/hex"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"zimam/agent/contract"
	"zimam/agent/internal/changes"
	"zimam/agent/internal/paths"
	"zimam/agent/internal/sysexec"
)

const (
	ufw       = "/usr/sbin/ufw"
	systemctl = "/usr/bin/systemctl"
)

func settings(ports ...contract.TCPPort) contract.SSHSettingsParams {
	return contract.SSHSettingsParams{RootLogin: "prohibit-password", Ports: ports, MaxAuthTries: 6, LoginGraceSeconds: 120}
}

func sshdT(ports ...string) sysexec.Script {
	out := ""
	for _, p := range ports {
		out += "port " + p + "\n"
	}
	return sysexec.Script{Path: sshd, Args: []string{"-T"}, Result: sysexec.Result{Stdout: []byte(out)}}
}

func call(path string, args ...string) sysexec.Script { return sysexec.Script{Path: path, Args: args} }

func ufwRule(port, comment string) string {
	return "### tuple ### allow tcp " + port + " 0.0.0.0/0 any 0.0.0.0/0 in comment=" + hex.EncodeToString([]byte(comment)) + "\n"
}

// newEnv is a server whose UFW has the locked ssh-22 rule (IPv6 off) and an
// earlier drop-in of Zimam's, when dropIn isn't "".
func newEnv(t *testing.T, dropIn string, scripts ...sysexec.Script) (sysexec.Deps, *sysexec.FakeRunner) {
	t.Helper()
	p := paths.Paths{Root: t.TempDir()}
	for _, dir := range []string{filepath.Dir(p.UFWUserRules()), filepath.Dir(p.UFWDefaults()), filepath.Dir(p.SSHDropIn()), p.LocksDir()} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	files := map[string]string{p.UFWUserRules(): ufwRule("22", "zimam:ssh-22"), p.UFWDefaults(): "IPV6=no\n"}
	if dropIn != "" {
		files[p.SSHDropIn()] = dropIn
	}
	for path, s := range files {
		if err := os.WriteFile(path, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	run := sysexec.NewFakeRunner(append(scripts, sysexec.Script{Path: "/usr/bin/systemd-run", AnyArgs: true}, sysexec.Script{Path: systemctl, AnyArgs: true})...)
	return sysexec.Deps{Paths: p, Run: run, Logf: t.Logf, Now: func() time.Time { return time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC) }}, run
}

// lines is each command run, as one line, timer calls left out.
func lines(run *sysexec.FakeRunner) []string {
	var out []string
	for _, c := range run.Calls() {
		if c.Path == "/usr/bin/systemd-run" || c.Path == systemctl && len(c.Args) > 0 && c.Args[0] == "stop" {
			continue
		}
		out = append(out, strings.Join(append([]string{filepath.Base(c.Path)}, c.Args...), " "))
	}
	return out
}

var add2222 = []string{"allow", "proto", "tcp", "from", "any", "to", "any", "port", "2222", "comment", "zimam:ssh-2222"}

func TestRootLoginYesNotRenderable(t *testing.T) {
	p := settings(22)
	p.RootLogin = "yes"
	if _, err := Render(p); err == nil {
		t.Error("Render accepted PermitRootLogin yes")
	}
	b, err := Render(settings(22, 2222))
	if err != nil || !strings.Contains(string(b), "Port 22\nPort 2222\n") || !strings.Contains(string(b), "PermitRootLogin prohibit-password\n") {
		t.Errorf("Render = %q, %v", b, err)
	}
}

// sshd keeps the first value it reads, so Zimam's drop-in must sort before
// Ubuntu's and cloud-init's (spike).
func TestDropInSortsFirst(t *testing.T) {
	names := []string{"50-cloud-init.conf", "60-cloudimg-settings.conf", filepath.Base(paths.Paths{}.SSHDropIn())}
	slices.Sort(names)
	if names[0] != "10-zimam.conf" {
		t.Errorf("sorted drop-ins = %v", names)
	}
}

func TestPortChangeAddsNewRuleFirstAndOldRuleAfterConfirm(t *testing.T) {
	d, run := newEnv(t, "",
		sshdT("22"),
		call(ufw, append([]string{"--dry-run"}, add2222...)...), call(ufw, add2222...),
		call(sshd, "-t"),
		call(systemctl, "is-active", "--quiet", "ssh.socket"),
		call(systemctl, "daemon-reload"), call(systemctl, "restart", "ssh.socket"),
		sshdT("2222"),
		call(ufw, "--dry-run", "delete", "allow", "proto", "tcp", "from", "any", "to", "any", "port", "22"),
		call(ufw, "delete", "allow", "proto", "tcp", "from", "any", "to", "any", "port", "22"),
	)
	id, deadline, err := Apply(context.Background(), d, settings(2222))
	if err != nil {
		t.Fatal(err)
	}
	if !deadline.Equal(d.Now().Add(Window)) {
		t.Errorf("deadline = %v", deadline)
	}
	b, _ := os.ReadFile(d.Paths.SSHDropIn())
	if !strings.Contains(string(b), "Port 2222\n") || strings.Contains(string(b), "Port 22\n") {
		t.Errorf("drop-in = %q", b)
	}
	before := lines(run)
	want := []string{
		"sshd -T",
		"ufw --dry-run " + strings.Join(add2222, " "), "ufw " + strings.Join(add2222, " "),
		"sshd -t",
		"systemctl is-active --quiet ssh.socket", "systemctl daemon-reload", "systemctl restart ssh.socket",
	}
	if !slices.Equal(before, want) {
		t.Errorf("before confirm:\n%s\nwant\n%s", strings.Join(before, "\n"), strings.Join(want, "\n"))
	}
	// The old port's rule goes only now.
	if err := changes.Confirm(context.Background(), d, id); err != nil {
		t.Fatal(err)
	}
	after := lines(run)[len(before):]
	if len(after) != 3 || after[0] != "sshd -T" || !strings.HasPrefix(after[2], "ufw delete allow proto tcp from any to any port 22") {
		t.Errorf("after confirm: %q", after)
	}
}

func TestPortChangeReloadsServiceWhenServiceActive(t *testing.T) {
	inactive := call(systemctl, "is-active", "--quiet", "ssh.socket")
	inactive.Result.ExitCode = 3
	d, run := newEnv(t, "",
		sshdT("22"),
		call(ufw, append([]string{"--dry-run"}, add2222...)...), call(ufw, add2222...),
		call(sshd, "-t"), inactive, call(systemctl, "reload", "ssh.service"),
	)
	if _, _, err := Apply(context.Background(), d, settings(2222)); err != nil {
		t.Fatal(err)
	}
	got := lines(run)
	if got[len(got)-1] != "systemctl reload ssh.service" || slices.Contains(got, "systemctl daemon-reload") {
		t.Errorf("calls = %q", got)
	}
}

func TestSettingsOnlyReloadService(t *testing.T) {
	d, run := newEnv(t, "", sshdT("22"), call(sshd, "-t"), call(systemctl, "reload", "ssh.service"))
	if _, _, err := Apply(context.Background(), d, settings(22)); err != nil {
		t.Fatal(err)
	}
	if got := lines(run); !slices.Equal(got, []string{"sshd -T", "sshd -t", "systemctl reload ssh.service"}) {
		t.Errorf("calls = %q", got)
	}
	if _, err := os.Stat(d.Paths.SSHDRunDir()); err != nil {
		t.Errorf("no %s for sshd -t: %v", d.Paths.SSHDRunDir(), err)
	}
}

func TestBadConfigRestoresPrevious(t *testing.T) {
	const old = "# the earlier settings\nPort 22\n"
	bad := call(sshd, "-t")
	bad.Result.ExitCode = 255
	bad.Result.Stderr = []byte("MaxAuthTries integer value invalid.")
	ok := call(sshd, "-t")
	d, _ := newEnv(t, old, sshdT("22"), bad, ok, call(systemctl, "reload", "ssh.service"), call(ufw, "reload"))
	_, _, err := Apply(context.Background(), d, settings(22))
	if err == nil || !strings.Contains(err.Error(), "integer value invalid") {
		t.Fatalf("Apply = %v, want the sshd -t failure", err)
	}
	if b, _ := os.ReadFile(d.Paths.SSHDropIn()); string(b) != old {
		t.Errorf("drop-in = %q, want the earlier one", b)
	}
	if entries, _ := os.ReadDir(d.Paths.ChangesDir()); len(entries) != 0 {
		t.Errorf("the failed change is still pending: %v", entries)
	}
}

// A new port something else holds fails ssh.socket's restart, which also
// stops ssh.service: the revert must restart the socket on the old ports,
// not reload a stopped service, or SSH stays down (seen on a VPS).
func TestFailedSocketRestartRevertRestartsSocket(t *testing.T) {
	busy := call(systemctl, "restart", "ssh.socket")
	busy.Result.ExitCode = 1
	failed := call(systemctl, "is-active", "--quiet", "ssh.socket")
	failed.Result.ExitCode = 3
	d, run := newEnv(t, "",
		sshdT("22"),
		call(ufw, append([]string{"--dry-run"}, add2222...)...), call(ufw, add2222...),
		call(sshd, "-t"), call(systemctl, "is-active", "--quiet", "ssh.socket"),
		call(systemctl, "daemon-reload"), busy,
		failed, call(systemctl, "is-failed", "--quiet", "ssh.socket"),
		call(systemctl, "restart", "ssh.socket"), call(ufw, "reload"),
	)
	if _, _, err := Apply(context.Background(), d, settings(22, 2222)); err == nil {
		t.Fatal("Apply succeeded with ssh.socket failing")
	}
	got := lines(run)
	i := slices.Index(got, "systemctl restart ssh.socket")
	want := []string{"systemctl restart ssh.socket", "sshd -t", "systemctl is-active --quiet ssh.socket",
		"systemctl is-failed --quiet ssh.socket", "systemctl daemon-reload", "systemctl restart ssh.socket", "ufw reload"}
	if i < 0 || !slices.Equal(got[i:], want) {
		t.Errorf("calls = %q, want them to end %q", got, want)
	}
	if _, err := os.Stat(d.Paths.SSHDropIn()); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("drop-in still there: %v", err)
	}
}

func TestRevertRestoresDropinsAndRules(t *testing.T) {
	d, run := newEnv(t, "",
		sshdT("22"),
		call(ufw, append([]string{"--dry-run"}, add2222...)...), call(ufw, add2222...),
		call(sshd, "-t"), call(systemctl, "is-active", "--quiet", "ssh.socket"),
		call(systemctl, "daemon-reload"), call(systemctl, "restart", "ssh.socket"),
		sshdT("2222"), call(ufw, "reload"),
	)
	rulesBefore, _ := os.ReadFile(d.Paths.UFWUserRules())
	id, _, err := Apply(context.Background(), d, settings(2222))
	if err != nil {
		t.Fatal(err)
	}
	// What the ufw call would have written.
	if err := os.WriteFile(d.Paths.UFWUserRules(), append(rulesBefore, ufwRule("2222", "zimam:ssh-2222")...), 0o644); err != nil {
		t.Fatal(err)
	}
	n := len(run.Calls())
	if err := changes.Revert(context.Background(), d, id); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(d.Paths.SSHDropIn()); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("drop-in still there: %v", err)
	}
	if b, _ := os.ReadFile(d.Paths.UFWUserRules()); string(b) != string(rulesBefore) {
		t.Errorf("rules = %q, want %q", b, rulesBefore)
	}
	var got []string
	for _, c := range run.Calls()[n:] {
		if c.Path != systemctl || c.Args[0] != "stop" {
			got = append(got, filepath.Base(c.Path)+" "+strings.Join(c.Args, " "))
		}
	}
	want := []string{"sshd -t", "systemctl is-active --quiet ssh.socket", "systemctl daemon-reload", "systemctl restart ssh.socket", "ufw reload"}
	if !slices.Equal(got, want) {
		t.Errorf("revert ran %q, want %q", got, want)
	}
}
