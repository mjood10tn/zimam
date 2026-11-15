package maintenance

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"zimam/agent/internal/php"
	"zimam/agent/internal/sysexec"
)

const (
	fpmActive84 = "/usr/bin/systemctl is-active --quiet php8.4-fpm"
	fpmTest84   = "/usr/sbin/php-fpm8.4 -t"
	fpmReload84 = "/usr/bin/systemctl reload php8.4-fpm"
)

func fpmActive(code int) script {
	return fixed(sysexec.Script{Path: "/usr/bin/systemctl", Args: []string{"is-active", "--quiet", "php8.4-fpm"}, Result: sysexec.Result{ExitCode: code}})
}

func fpmTest() script {
	return fixed(sysexec.Script{Path: "/usr/sbin/php-fpm8.4", Args: []string{"-t"}})
}

func fpmReload(code int) script {
	return fixed(sysexec.Script{Path: "/usr/bin/systemctl", Args: []string{"reload", "php8.4-fpm"}, Result: sysexec.Result{ExitCode: code}})
}

// installPHP84 puts PHP 8.4's FPM binary and a site pool in place.
func (e *env) installPHP84(pool bool) {
	e.t.Helper()
	p := e.d.Paths
	for _, f := range []string{p.PHPFPMBinary("8.4"), filepath.Join(p.PHPPoolDir("8.4"), "zimam-shop.conf")} {
		if !pool && filepath.Dir(f) == p.PHPPoolDir("8.4") {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
			e.t.Fatal(err)
		}
		if err := os.WriteFile(f, nil, 0o644); err != nil {
			e.t.Fatal(err)
		}
	}
}

func (e *env) mark() {
	e.t.Helper()
	if err := php.MarkReload(e.d.Paths, "8.4"); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) marked() bool {
	_, err := os.Stat(e.d.Paths.FPMReloadMark("8.4"))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		e.t.Fatal(err)
	}
	return err == nil
}

// TestMaintenanceReloadsMarkedPHP (spike amendment A3): a version a deploy
// marked is tested and reloaded gracefully, under the reload lock, and its
// mark cleared; the run is audited.
func TestMaintenanceReloadsMarkedPHP(t *testing.T) {
	e := newEnv(t, fpmActive(0), fpmTest(), fpmReload(0))
	e.installPHP84(true)
	e.mark()
	if err := e.maintain(); err != nil {
		t.Fatal(err)
	}
	lines := e.commandLines()
	i := slices.Index(lines, fpmActive84)
	if i < 0 || i+2 >= len(lines) || lines[i+1] != fpmTest84 || lines[i+2] != fpmReload84 {
		t.Errorf("commands\n%q\nwant is-active, php-fpm -t, then reload", lines)
	}
	if e.marked() {
		t.Error("the mark is still there")
	}
	entries := auditEntries(t, e.d.Paths)
	if len(entries) != 1 {
		t.Fatalf("audit entries %v, want 1", entries)
	}
	params, _ := entries[0]["params"].(map[string]any)
	if got, _ := params["reloaded_php"].([]any); len(got) != 1 || got[0] != "8.4" {
		t.Errorf("audit params %v, want reloaded_php [8.4]", params)
	}
}

// A failed reload keeps the mark, for the next run.
func TestMaintenanceKeepsMarkWhenReloadFails(t *testing.T) {
	e := newEnv(t, fpmActive(0), fpmTest(), fpmReload(1))
	e.installPHP84(true)
	e.mark()
	if err := e.maintain(); err == nil {
		t.Error("maintenance passed with the reload failing")
	}
	if !e.marked() {
		t.Error("the mark is gone after a failed reload")
	}
}

// Nothing to reload: a version that isn't installed, has no pool or whose
// service doesn't run. The mark goes, so it doesn't fail every run.
func TestMaintenanceDropsMarkWithNothingToReload(t *testing.T) {
	for _, c := range []struct {
		name            string
		installed, pool bool
		first           []script
	}{
		{"not installed", false, false, nil},
		{"no pool", true, false, nil},
		{"inactive", true, true, []script{fpmActive(3)}},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t, c.first...)
			if c.installed {
				e.installPHP84(c.pool)
			}
			e.mark()
			if err := e.maintain(); err != nil {
				t.Fatal(err)
			}
			if e.marked() {
				t.Error("the mark is still there")
			}
			if slices.Contains(e.commandLines(), fpmReload84) {
				t.Error("maintenance reloaded")
			}
		})
	}
}

// A mark that isn't a supported version's file is dropped too.
func TestMaintenanceDropsBadMarks(t *testing.T) {
	e := newEnv(t)
	dir := e.d.Paths.FPMReloadDir()
	if err := os.MkdirAll(filepath.Join(dir, "subdir"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "7.4"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := e.maintain(); err != nil {
		t.Fatal(err)
	}
	if es, _ := os.ReadDir(dir); len(es) != 0 {
		t.Errorf("%s still holds %v", dir, es)
	}
	if e.ran("/usr/bin/systemctl") {
		t.Errorf("ran systemctl: %q", e.commandLines())
	}
}
