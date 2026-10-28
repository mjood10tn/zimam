package nginx

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"zimam/agent/internal/configtx"
	"zimam/agent/internal/paths"
	"zimam/agent/internal/sysexec"
)

var (
	nginxTest   = sysexec.Script{Path: "/usr/sbin/nginx", Args: []string{"-t"}}
	reloadNginx = sysexec.Script{Path: "/usr/bin/systemctl", Args: []string{"reload-or-restart", "nginx"}}
)

func line(s sysexec.Script) string { return strings.Join(append([]string{s.Path}, s.Args...), " ") }

func commands(run *sysexec.FakeRunner) []string {
	var lines []string
	for _, c := range run.Calls() {
		lines = append(lines, strings.Join(append([]string{c.Path}, c.Args...), " "))
	}
	return lines
}

// newDeps is a server tree under a temporary root, with Nginx's directories
// and the locks directory, and a FakeRunner answering scripts.
func newDeps(t *testing.T, scripts ...sysexec.Script) (sysexec.Deps, *sysexec.FakeRunner) {
	t.Helper()
	p := paths.Paths{Root: t.TempDir()}
	for _, dir := range []string{p.LocksDir(), filepath.Dir(p.NginxPanelAvailable()), filepath.Dir(p.NginxPanelEnabled())} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	run := sysexec.NewFakeRunner(scripts...)
	return sysexec.Deps{Paths: p, Run: run, Logf: t.Logf}, run
}

func panelSite(d sysexec.Deps, content string) ([]configtx.Change, Link) {
	return []configtx.Change{configtx.RootFile(d.Paths.NginxPanelAvailable(), []byte(content), 0o644)},
		Link{Path: d.Paths.NginxPanelEnabled(), Target: d.Paths.NginxPanelAvailable()}
}

func TestApplySiteEnablesTestsAndReloads(t *testing.T) {
	d, run := newDeps(t, nginxTest, reloadNginx)
	changes, link := panelSite(d, "server {}\n")
	if err := ApplySite(context.Background(), d, changes, link); err != nil {
		t.Fatalf("ApplySite: %v", err)
	}
	if target, err := os.Readlink(link.Path); err != nil || target != link.Target {
		t.Errorf("sites-enabled link = %q, %v; want %q", target, err, link.Target)
	}
	if b, err := os.ReadFile(link.Target); err != nil || string(b) != "server {}\n" {
		t.Errorf("vhost = %q, %v", b, err)
	}
	if got, want := commands(run), []string{line(nginxTest), line(reloadNginx)}; !slices.Equal(got, want) {
		t.Errorf("ran %q, want %q", got, want)
	}
}

// linkWatcher records, at each command it runs, whether the link exists.
type linkWatcher struct {
	sysexec.Runner
	link string
	seen []bool
}

func (w *linkWatcher) Run(ctx context.Context, c sysexec.Cmd) (sysexec.Result, error) {
	_, err := os.Lstat(w.link)
	w.seen = append(w.seen, err == nil)
	return w.Runner.Run(ctx, c)
}

func TestApplySiteRestoresLinkOnFailure(t *testing.T) {
	bad := nginxTest
	bad.Result = sysexec.Result{ExitCode: 1, Stderr: []byte(`nginx: [emerg] unknown directive "servr" in /etc/nginx/sites-enabled/zimam-panel.conf:1`)}
	d, run := newDeps(t, bad, nginxTest)
	changes, link := panelSite(d, "servr {}\n")
	watcher := &linkWatcher{Runner: run, link: link.Path}
	d.Run = watcher
	err := ApplySite(context.Background(), d, changes, link)
	var failed *configtx.TestFailed
	if !errors.As(err, &failed) || !strings.Contains(err.Error(), `unknown directive "servr"`) {
		t.Fatalf("ApplySite = %v, want a test failure with nginx's output", err)
	}
	for _, path := range []string{link.Path, link.Target} {
		if _, err := os.Lstat(path); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("%s is still there (Lstat: %v)", path, err)
		}
	}
	if got, want := commands(run), []string{line(nginxTest), line(nginxTest)}; !slices.Equal(got, want) {
		t.Errorf("ran %q, want the test, then the retest, and no reload", got)
	}
	// The test saw the new site enabled; the retest ran without the link,
	// which would otherwise dangle.
	if want := []bool{true, false}; !slices.Equal(watcher.seen, want) {
		t.Errorf("the link existed during the tests: %v, want %v", watcher.seen, want)
	}
}

func TestApplySiteKeepsAnExistingLink(t *testing.T) {
	bad := nginxTest
	bad.Result = sysexec.Result{ExitCode: 1, Stderr: []byte("nginx: [emerg] bad")}
	d, _ := newDeps(t, bad, nginxTest)
	changes, link := panelSite(d, "servr {}\n")
	if err := os.WriteFile(link.Target, []byte("server {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(link.Target, link.Path); err != nil {
		t.Fatal(err)
	}
	if err := ApplySite(context.Background(), d, changes, link); err == nil {
		t.Fatal("ApplySite succeeded although nginx -t failed")
	}
	if target, err := os.Readlink(link.Path); err != nil || target != link.Target {
		t.Errorf("the existing link was changed: %q, %v", target, err)
	}
	if b, _ := os.ReadFile(link.Target); string(b) != "server {}\n" {
		t.Errorf("vhost = %q, want the previous one back", b)
	}
}

func TestApplySiteLeavesADisabledSiteDisabled(t *testing.T) {
	// The vhost exists but isn't enabled. After a failed test it is back
	// as it was, and so is sites-enabled: no link.
	bad := nginxTest
	bad.Result = sysexec.Result{ExitCode: 1, Stderr: []byte("nginx: [emerg] bad")}
	d, _ := newDeps(t, bad, nginxTest)
	changes, link := panelSite(d, "servr {}\n")
	if err := os.WriteFile(link.Target, []byte("server {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ApplySite(context.Background(), d, changes, link); err == nil {
		t.Fatal("ApplySite succeeded although nginx -t failed")
	}
	if _, err := os.Lstat(link.Path); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the site was left enabled (Lstat: %v)", err)
	}
	if b, _ := os.ReadFile(link.Target); string(b) != "server {}\n" {
		t.Errorf("vhost = %q, want the previous one back", b)
	}
}

func TestApplySiteRefusesForeignLink(t *testing.T) {
	d, run := newDeps(t, nginxTest, reloadNginx)
	changes, link := panelSite(d, "server {}\n")
	if err := os.Symlink("/etc/shadow", link.Path); err != nil {
		t.Fatal(err)
	}
	if err := ApplySite(context.Background(), d, changes, link); err == nil {
		t.Fatal("ApplySite replaced a link that points elsewhere")
	}
	if _, err := os.Lstat(link.Target); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the vhost was written (Lstat: %v)", err)
	}
	if got := commands(run); len(got) != 0 {
		t.Errorf("ran %q", got)
	}
}

func TestReloadTakesTheLock(t *testing.T) {
	d, run := newDeps(t, reloadNginx)
	unlock, err := sysexec.Lock(context.Background(), d.Paths.ReloadLock())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if err := Reload(ctx, d); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Reload with the lock held = %v, want it to wait", err)
	}
	if got := commands(run); len(got) != 0 {
		t.Errorf("ran %q while another process held the lock", got)
	}
	unlock()
	if err := Reload(context.Background(), d); err != nil {
		t.Fatalf("Reload: %v", err)
	}
	if got := commands(run); !slices.Equal(got, []string{line(reloadNginx)}) {
		t.Errorf("ran %q", got)
	}
}
