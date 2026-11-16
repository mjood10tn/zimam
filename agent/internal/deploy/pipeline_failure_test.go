package deploy

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"zimam/agent/contract"
	"zimam/agent/internal/helper"
	"zimam/agent/internal/paths"
)

// Deploys that fail part way, or are cancelled: what stays live, and what
// is cleaned up.

// wrapHook runs f before the env's hook; f answers when it returns true.
func (e *deployEnv) wrapHook(f func(a helper.Account, r helper.ExecRequest) (int, bool)) {
	hook := e.h.hook
	e.h.hook = func(a helper.Account, r helper.ExecRequest) (int, bool) {
		if code, ok := f(a, r); ok {
			return code, true
		}
		return hook(a, r)
	}
}

func stateBytes(t *testing.T, p paths.Paths) string {
	t.Helper()
	return readFile(t, p.SiteState("shop"))
}

// TestSwapErrorAfterRenameKeepsNewReleaseLive: a swap can report an error
// after its rename landed (a cancel seen late, a failed directory sync).
// current then points at the new release, which must stay: removing it
// would take the site down. The deploy reports it live, records it and
// carries on.
func TestSwapErrorAfterRenameKeepsNewReleaseLive(t *testing.T) {
	e := newDeployEnv(t, contract.SiteStatic)
	e.up.commit(map[string]string{"index.html": "new"})
	e.h.swapThenFail = map[string]error{"site/current": errors.New("syncing site/: input/output error")}
	res, err := e.deploy(contract.SiteDeployParams{})
	if err == nil || !strings.Contains(err.Error(), "is live, but") || !strings.Contains(err.Error(), "input/output error") {
		t.Fatalf("deploy = %v; want the swap's error, with the release live", err)
	}
	if res.Release == "" || e.current() != "releases/"+string(res.Release) {
		t.Fatalf("result %+v, current -> %s", res, e.current())
	}
	if got := readFile(t, e.home("site/current/index.html")); got != "new" {
		t.Errorf("the site serves %q", got)
	}
	if st := e.state(); len(st.Releases) != 1 || st.Releases[0].ID != res.Release || !st.Releases[0].Active {
		t.Errorf("state releases %+v", st.Releases)
	}
}

// A swap that failed before its rename leaves current on the old release,
// and the new one goes.
func TestSwapFailureDiscardsNewRelease(t *testing.T) {
	e := newDeployEnv(t, contract.SiteStatic)
	e.up.commit(map[string]string{"index.html": "new"})
	before := stateBytes(t, e.p)
	e.h.failSwap = map[string]error{"site/current": errors.New("rename: no space left on device")}
	if _, err := e.deploy(contract.SiteDeployParams{}); err == nil || strings.Contains(err.Error(), "is live") {
		t.Fatalf("deploy = %v; want a failure with nothing live", err)
	}
	if !slices.Equal(e.releases(), []string{"initial"}) || e.current() != initialRel || stateBytes(t, e.p) != before {
		t.Errorf("releases %q, current %s", e.releases(), e.current())
	}
}

// TestCancelledDeployCleansUp: a job cancelled mid-build ends with the
// cancel as its error, and its cleanup still runs: the new release (or a
// first deploy's half-made repository) goes; current and the state stay.
func TestCancelledDeployCleansUp(t *testing.T) {
	for _, c := range []struct{ name, program, arg string }{
		{"in the script", "deploy-script", ""},
		{"in Composer", "composer", "install"},
		{"in git remote add", "git", "remote"},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := newDeployEnv(t, contract.SitePHP)
			e.up.commit(map[string]string{"composer.json": "{}"})
			before := stateBytes(t, e.p)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			e.wrapHook(func(_ helper.Account, r helper.ExecRequest) (int, bool) {
				if r.Program == c.program && (c.arg == "" || (len(r.Args) > 0 && r.Args[0] == c.arg)) {
					cancel()
					return 137, true
				}
				return 0, false
			})
			_, err := Run(ctx, e.d, e.h, e.lookup, contract.SiteDeployParams{Name: "shop", Trigger: contract.DeployManual, Composer: true, Script: "true\n"})
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("deploy = %v; want the cancel", err)
			}
			if !slices.Equal(e.releases(), []string{"initial"}) || e.current() != initialRel {
				t.Errorf("releases %q, current %s", e.releases(), e.current())
			}
			if stateBytes(t, e.p) != before {
				t.Error("the state changed")
			}
			if _, err := os.Lstat(e.home("site/repo.git")); c.program == "git" && !errors.Is(err, fs.ErrNotExist) {
				t.Errorf("the half-made site/repo.git is left: %v", err)
			}
		})
	}
}

// TestFailureAfterSwapReportsLiveRelease: once current points at the new
// release, a failure (recording it, marking PHP, pruning) is reported with
// the result, and the release stays live.
func TestFailureAfterSwapReportsLiveRelease(t *testing.T) {
	for _, c := range []struct {
		name  string
		setup func(e *deployEnv)
		want  string
	}{
		{"save", func(e *deployEnv) {
			// The state file turns into a directory while the script runs,
			// after the deploy read it: the rename over it fails, even as root.
			e.wrapHook(func(_ helper.Account, r helper.ExecRequest) (int, bool) {
				if r.Program == "deploy-script" {
					os.Remove(e.p.SiteState("shop"))
					os.Mkdir(e.p.SiteState("shop"), 0o700)
				}
				return 0, false
			})
		}, "recording it failed"},
		{"mark", func(e *deployEnv) {
			os.MkdirAll(filepath.Dir(e.p.FPMReloadDir()), 0o755)
			os.WriteFile(e.p.FPMReloadDir(), nil, 0o600) // a file where the directory goes
		}, "marking PHP 8.4"},
		{"prune", func(e *deployEnv) {
			e.h.failRemove = map[string]error{"site/releases/initial": errors.New("remove-tree: permission denied")}
		}, "pruning"},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := newDeployEnv(t, contract.SitePHP)
			e.up.commit(map[string]string{"index.php": "one"})
			if c.name == "prune" {
				e.mustDeploy(contract.SiteDeployParams{}) // initial is pruned from the second deploy on
				e.up.commit(map[string]string{"index.php": "two"})
			}
			c.setup(e)
			res, err := e.deploy(contract.SiteDeployParams{Script: "true\n"})
			if err == nil || !strings.Contains(err.Error(), "is live, but") || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("deploy = %v; want %q with the release live", err, c.want)
			}
			if res.Release == "" || e.current() != "releases/"+string(res.Release) {
				t.Errorf("result %+v, current -> %s", res, e.current())
			}
		})
	}
}

// TestFetchRecoversRepository: a site repository that lost its origin, has
// another one, or holds the branch's lock file from a killed fetch, still
// fetches.
func TestFetchRecoversRepository(t *testing.T) {
	for _, c := range []struct {
		name   string
		damage func(t *testing.T, e *deployEnv)
	}{
		{"no origin", func(t *testing.T, e *deployEnv) { gitIn(t, e.home("site/repo.git"), "remote", "remove", "origin") }},
		{"other origin", func(t *testing.T, e *deployEnv) {
			gitIn(t, e.home("site/repo.git"), "remote", "set-url", "origin", "git@github.com:octo/old.git")
		}},
		{"stale lock", func(t *testing.T, e *deployEnv) {
			if err := os.WriteFile(e.home("site/repo.git/refs/heads/main.lock"), nil, 0o640); err != nil {
				t.Fatal(err)
			}
		}},
		{"open mode", func(t *testing.T, e *deployEnv) {
			// What a deploy before the Site layout's 0700 left.
			if err := os.Chmod(e.home("site/repo.git"), 0o750); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := newDeployEnv(t, contract.SiteStatic)
			e.up.commit(map[string]string{"index.html": "one"})
			e.mustDeploy(contract.SiteDeployParams{})
			c.damage(t, e)
			e.up.commit(map[string]string{"index.html": "two"})
			e.mustDeploy(contract.SiteDeployParams{})
			if got := readFile(t, e.home("site/current/index.html")); got != "two" {
				t.Errorf("index.html = %q", got)
			}
			if url := gitIn(t, e.home("site/repo.git"), "config", "remote.origin.url"); url != "git@github.com:octo/app.git" {
				t.Errorf("origin is %q", url)
			}
			checkModeOf(t, e.home("site/repo.git"), fs.ModeDir|0o700)
		})
	}
}

// TestExportReportsHelperFailure: when git archive's helper fails without
// a result, extract-tar only sees the archive end early; the deploy reports
// the helper's failure.
func TestExportReportsHelperFailure(t *testing.T) {
	e := newDeployEnv(t, contract.SiteStatic)
	e.up.commit(map[string]string{"index.html": "one"})
	e.h.failExec = map[string]error{"git archive": errors.New("as-user exec git as s_shop: the helper ended without a result")}
	_, err := e.deploy(contract.SiteDeployParams{})
	if err == nil || !strings.Contains(err.Error(), "the helper ended without a result") {
		t.Fatalf("deploy = %v; want the helper's failure", err)
	}
	if !slices.Equal(e.releases(), []string{"initial"}) {
		t.Errorf("releases %q", e.releases())
	}
}

// TestMergeBaseFailureIsNotValidation: only "not an ancestor" (exit 1) and
// an unknown commit are the caller's mistake; any other git failure is a
// plain error.
func TestMergeBaseFailureIsNotValidation(t *testing.T) {
	e := newDeployEnv(t, contract.SiteStatic)
	sha := e.up.commit(map[string]string{"index.html": "one"})
	e.wrapHook(func(_ helper.Account, r helper.ExecRequest) (int, bool) {
		if r.Program == "git" && r.Args[0] == "merge-base" {
			r.Log("fatal: unable to read tree: input/output error")
			return 128, true
		}
		return 0, false
	})
	_, err := e.deploy(contract.SiteDeployParams{Commit: &sha})
	var ae *contract.AgentError
	if err == nil || errors.As(err, &ae) {
		t.Errorf("deploy = %v; want a plain error, not %v", err, ae)
	}
}
