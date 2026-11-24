package cloudflare

import (
	"context"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"zimam/agent/contract"
	"zimam/agent/internal/paths"
	"zimam/agent/internal/sysexec"
)

const (
	nginxBin   = "/usr/sbin/nginx"
	systemctl  = "/usr/bin/systemctl"
	ufw        = "/usr/sbin/ufw"
	systemdRun = "/usr/bin/systemd-run"
)

// fakeHTTP answers each URL with a body, or fails for a URL it doesn't know.
type fakeHTTP map[string]string

func (f fakeHTTP) RoundTrip(r *http.Request) (*http.Response, error) {
	body, ok := f[r.URL.String()]
	if !ok {
		return nil, errors.New("network down")
	}
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}, Request: r}, nil
}

type env struct {
	d   sysexec.Deps
	run *sysexec.FakeRunner
	now time.Time
}

func newEnv(t *testing.T, web fakeHTTP, scripts ...sysexec.Script) *env {
	t.Helper()
	p := paths.Paths{Root: t.TempDir()}
	for _, dir := range []string{filepath.Dir(p.NginxRealIPConf()), filepath.Dir(p.UFWUserRules()), filepath.Dir(p.UFWDefaults()), p.LocksDir(), p.StateDir(), p.ConfigTxDir()} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	e := &env{run: sysexec.NewFakeRunner(scripts...), now: time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)}
	e.d = sysexec.Deps{Paths: p, Run: e.run, Logf: t.Logf, Now: func() time.Time { return e.now }, HTTP: &http.Client{Transport: web}}
	e.writeRules("", "")
	if err := os.WriteFile(p.UFWDefaults(), []byte("IPV6=yes\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return e
}

func (e *env) writeRules(v4, v6 string) {
	for path, body := range map[string]string{e.d.Paths.UFWUserRules(): v4, e.d.Paths.UFWUser6Rules(): v6} {
		if err := os.WriteFile(path, []byte("*filter\n### RULES ###\n"+body+"### END RULES ###\n"), 0o640); err != nil {
			panic(err)
		}
	}
}

func tupleLine(port, src, comment string) string {
	dst := "0.0.0.0/0"
	if strings.Contains(src, ":") {
		dst = "::/0"
	}
	return "### tuple ### allow tcp " + port + " " + dst + " any " + src + " in comment=" + hex.EncodeToString([]byte(comment)) + "\n"
}

func nginxOK() []sysexec.Script {
	return []sysexec.Script{
		{Path: nginxBin, Args: []string{"-t"}},
		{Path: systemctl, Args: []string{"reload-or-restart", "nginx"}},
	}
}

// lines builds a ranges body as Cloudflare serves it: no trailing newline.
func lines(n int, v6 bool) string {
	var out []string
	for i := range n {
		if v6 {
			out = append(out, netip.PrefixFrom(netip.AddrFrom16([16]byte{0x3f, 0xff, 0x00, byte(i)}), 32).String())
		} else {
			out = append(out, netip.PrefixFrom(netip.AddrFrom4([4]byte{198, 18, byte(i), 0}), 24).String())
		}
	}
	return strings.Join(out, "\n")
}

func cfWeb(v4, v6 string) fakeHTTP { return fakeHTTP{URLv4: v4, URLv6: v6} }

func (e *env) enable(t *testing.T) {
	t.Helper()
	if err := os.WriteFile(e.d.Paths.NginxRealIPConf(), []byte("old\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestCompiledRangesAreValid(t *testing.T) {
	r := compiled()
	if len(r.V4) != 15 || len(r.V6) != 7 {
		t.Errorf("compiled: %d v4, %d v6", len(r.V4), len(r.V6))
	}
	for _, p := range append(slices.Clone(r.V4), r.V6...) {
		if p != p.Masked() {
			t.Errorf("%v isn't canonical", p)
		}
	}
}

func TestRefreshRefusesBadLine(t *testing.T) {
	for name, body := range map[string]string{
		"host bits":    lines(10, false) + "\n198.18.0.1/24",
		"no prefix":    lines(10, false) + "\n198.18.99.0",
		"other text":   lines(10, false) + "\n<html>",
		"wrong family": lines(10, false) + "\n2400:cb00::/32",
		"too few":      lines(4, false),
		"too many":     lines(101, false),
		"blank line":   lines(5, false) + "\n\n" + lines(5, false),
	} {
		e := newEnv(t, cfWeb(body, lines(7, true)))
		e.enable(t)
		changed, err := Refresh(context.Background(), e.d)
		if err != nil || changed {
			t.Errorf("%s: Refresh = %v, %v; want the compiled list kept and no error", name, changed, err)
		}
		s := readState(t, e.d.Paths)
		if s.Error == "" || !s.RefreshedAt.IsZero() || len(s.V4) != 0 {
			t.Errorf("%s: state = %+v, want the failure recorded", name, s)
		}
	}
}

func TestRefreshRefusesShrunkList(t *testing.T) {
	// The compiled list has 15 v4 ranges; 7 is less than half.
	e := newEnv(t, cfWeb(lines(7, false), lines(7, true)))
	e.enable(t)
	if changed, err := Refresh(context.Background(), e.d); err != nil || changed {
		t.Fatalf("Refresh = %v, %v", changed, err)
	}
	if s := readState(t, e.d.Paths); !strings.Contains(s.Error, "shrank") {
		t.Errorf("error = %q", s.Error)
	}
}

func TestRefreshKeepsCompiledListOnNetworkError(t *testing.T) {
	e := newEnv(t, fakeHTTP{})
	e.enable(t)
	if changed, err := Refresh(context.Background(), e.d); err != nil || changed {
		t.Fatalf("Refresh = %v, %v", changed, err)
	}
	r, at, err := Current(e.d.Paths)
	if err != nil || !slices.Equal(r.V4, compiled().V4) || !at.Equal(compiledAt) {
		t.Errorf("Current = %v, %v, %v; want the compiled list", r.V4, at, err)
	}
	if b, _ := os.ReadFile(e.d.Paths.NginxRealIPConf()); string(b) != "old\n" {
		t.Errorf("realip conf changed to %q", b)
	}
}

func TestRefreshOnlyInCloudflareModeAndWeekly(t *testing.T) {
	e := newEnv(t, cfWeb(lines(15, false), lines(7, true)))
	if changed, err := Refresh(context.Background(), e.d); err != nil || changed || len(e.run.Calls()) != 0 {
		t.Fatalf("mode off: Refresh = %v, %v", changed, err)
	}
	if _, err := os.Stat(e.d.Paths.CloudflareRanges()); err == nil {
		t.Error("mode off: Refresh wrote the ranges")
	}
	e.enable(t)
	e.run.Add(nginxOK()[0])
	e.run.Add(nginxOK()[1])
	if changed, err := Refresh(context.Background(), e.d); err != nil || !changed {
		t.Fatalf("Refresh = %v, %v", changed, err)
	}
	conf, _ := os.ReadFile(e.d.Paths.NginxRealIPConf())
	if !strings.Contains(string(conf), "set_real_ip_from 198.18.14.0/24;") || !strings.Contains(string(conf), "set_real_ip_from 3fff:6::/32;") {
		t.Errorf("realip conf:\n%s", conf)
	}
	calls := len(e.run.Calls())
	e.now = e.now.Add(6 * 24 * time.Hour)
	if changed, err := Refresh(context.Background(), e.d); err != nil || changed || len(e.run.Calls()) != calls {
		t.Errorf("six days later: Refresh = %v, %v, want nothing", changed, err)
	}
	r, at, _ := Current(e.d.Paths)
	if len(r.V4) != 15 || !at.Equal(time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)) {
		t.Errorf("Current = %d v4, %v", len(r.V4), at)
	}
}

func TestRefreshReappliesRestriction(t *testing.T) {
	e := newEnv(t, cfWeb(lines(15, false), lines(7, true)), nginxOK()...)
	e.enable(t)
	var v4, v6 string
	for _, p := range compiled().V4 {
		v4 += tupleLine("80", p.String(), "zimam:http") + tupleLine("443", p.String(), "zimam:https")
	}
	for _, p := range compiled().V6 {
		v6 += tupleLine("80", p.String(), "zimam:http") + tupleLine("443", p.String(), "zimam:https")
	}
	e.writeRules(v4, v6)
	e.run.Add(sysexec.Script{Path: ufw, AnyArgs: true})
	if changed, err := Refresh(context.Background(), e.d); err != nil || !changed {
		t.Fatalf("Refresh = %v, %v", changed, err)
	}
	var adds, deletes int
	for _, c := range e.run.Calls() {
		switch {
		case c.Path == systemdRun:
			t.Error("a refresh started a guarded change")
		case c.Path == ufw && c.Args[0] == "allow":
			adds++
		case c.Path == ufw && c.Args[0] == "delete":
			deletes++
		}
	}
	// Every new range on both ports, every old one gone.
	if adds != 2*(15+7) || deletes != 2*(15+7) {
		t.Errorf("%d adds, %d deletes", adds, deletes)
	}
}

func TestOffRemovesConf(t *testing.T) {
	e := newEnv(t, nil, nginxOK()...)
	e.enable(t)
	res, err := Mode(context.Background(), e.d, contract.CloudflareModeParams{})
	if err != nil || res.Enabled || res.Change != nil {
		t.Fatalf("Mode = %+v, %v", res, err)
	}
	if _, err := os.Stat(e.d.Paths.NginxRealIPConf()); err == nil {
		t.Error("the realip conf is still there")
	}
	// Off again changes nothing and reloads nothing.
	calls := len(e.run.Calls())
	if _, err := Mode(context.Background(), e.d, contract.CloudflareModeParams{}); err != nil || len(e.run.Calls()) != calls {
		t.Errorf("Mode off twice = %v, ran %v", err, e.run.Calls()[calls:])
	}
}

func TestOnWritesConfWithoutGuard(t *testing.T) {
	e := newEnv(t, nil, nginxOK()...)
	res, err := Mode(context.Background(), e.d, contract.CloudflareModeParams{Enabled: true})
	if err != nil || !res.Enabled || res.Change != nil || res.Deadline != nil {
		t.Fatalf("Mode = %+v, %v", res, err)
	}
	conf, _ := os.ReadFile(e.d.Paths.NginxRealIPConf())
	if !strings.Contains(string(conf), "set_real_ip_from 173.245.48.0/20;") || !strings.Contains(string(conf), "real_ip_header CF-Connecting-IP;") {
		t.Errorf("realip conf:\n%s", conf)
	}
}

func TestBadConfRestoresPrevious(t *testing.T) {
	bad := sysexec.Script{Path: nginxBin, Args: []string{"-t"}, Result: sysexec.Result{ExitCode: 1}}
	e := newEnv(t, nil, bad)
	_, err := Mode(context.Background(), e.d, contract.CloudflareModeParams{Enabled: true})
	if err == nil {
		t.Fatal("Mode succeeded after nginx -t failed")
	}
	if _, err := os.Stat(e.d.Paths.NginxRealIPConf()); err == nil {
		t.Error("the refused conf stayed")
	}
}

func TestRestrictOriginIsGuarded(t *testing.T) {
	e := newEnv(t, nil, append(nginxOK(),
		sysexec.Script{Path: systemdRun, AnyArgs: true},
		sysexec.Script{Path: ufw, AnyArgs: true})...)
	res, err := Mode(context.Background(), e.d, contract.CloudflareModeParams{Enabled: true, RestrictOrigin: true})
	if err != nil || !res.RestrictOrigin || res.Change == nil || res.Deadline == nil {
		t.Fatalf("Mode = %+v, %v", res, err)
	}
	if _, err := os.Stat(e.d.Paths.ChangeRecord(*res.Change)); err != nil {
		t.Errorf("no change record: %v", err)
	}
	var sawTimer bool
	for _, c := range e.run.Calls() {
		if c.Path == systemdRun {
			sawTimer = true
		}
		if c.Path == ufw && c.Args[0] == "allow" && !sawTimer {
			t.Errorf("ufw %q ran before the revert timer", c.Args)
		}
	}
	if !sawTimer {
		t.Error("no revert timer")
	}
}

func TestSameRestrictionNeedsNoGuard(t *testing.T) {
	e := newEnv(t, nil, nginxOK()...)
	e.writeRules(tupleLine("443", "173.245.48.0/20", "zimam:https"), "")
	res, err := Mode(context.Background(), e.d, contract.CloudflareModeParams{Enabled: true, RestrictOrigin: true})
	if err != nil || res.Change != nil {
		t.Errorf("Mode = %+v, %v; want no change to confirm", res, err)
	}
}

func readState(t *testing.T, p paths.Paths) state {
	t.Helper()
	s, err := read(p)
	if err != nil {
		t.Fatal(err)
	}
	return s
}
