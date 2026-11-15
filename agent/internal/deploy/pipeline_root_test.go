//go:build roottest

package deploy

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"zimam/agent/contract"
	"zimam/agent/internal/config"
	"zimam/agent/internal/helper"
	"zimam/agent/internal/paths"
	"zimam/agent/internal/sites"
	"zimam/agent/internal/sysexec"
)

const deploySite = "deploytest"

// TestBackgroundedScriptProcessEndsWithJob deploys for real, as the site
// user through the real helper: git fetch and archive streamed into
// extract-tar (A1), and a deploy script that leaves `sleep 600 &` behind.
// When the deploy returns, no process of the user is left, and the release
// is the user's, with the script's execute bit (A4).
func TestBackgroundedScriptProcessEndsWithJob(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Fatal("the roottest tests need root")
	}
	user := "s_" + deploySite
	exec.Command("/usr/sbin/userdel", "--remove", user).Run() // left over from an earlier run
	if out, err := exec.Command("/usr/sbin/useradd", "--create-home", "--user-group", "--shell", "/usr/sbin/nologin", user).CombinedOutput(); err != nil {
		t.Fatalf("useradd: %v\n%s", err, out)
	}
	t.Cleanup(func() {
		exec.Command("/usr/bin/pkill", "-KILL", "-u", user).Run()
		exec.Command("/usr/sbin/userdel", "--remove", user).Run()
	})
	a, err := helper.LookupAccount(user)
	if err != nil {
		t.Fatal(err)
	}
	run := func(name string, args ...string) {
		t.Helper()
		if out, err := exec.Command(name, args...).CombinedOutput(); err != nil {
			t.Fatalf("%s %q: %v\n%s", name, args, err, out)
		}
	}

	// GitHub stands in as a repository of the user's own (git refuses one
	// another user owns), which the user's ~/.gitconfig sends the remote to.
	upstream, err := os.MkdirTemp("", "zimam-upstream-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(upstream) })
	u := &upstreamRepo{t: t, dir: upstream}
	u.git("init", "-q", "-b", "main")
	sha := u.commit(map[string]string{"public/index.html": "hello", "bin/run.sh": "#!/bin/sh\n"})
	run("/usr/bin/chmod", "0755", filepath.Join(upstream, "bin/run.sh"))
	u.git("add", "-A")
	u.git("commit", "-q", "--amend", "-m", "exec bit")
	sha = contract.CommitSHA(u.git("rev-parse", "HEAD"))
	run("/usr/bin/chmod", "0755", upstream)
	run("/usr/bin/chown", "-R", user+":"+user, upstream)
	gitconfig := "[url \"" + upstream + "\"]\n\tinsteadOf = " + RemoteURL("octo/app") + "\n"
	if err := os.WriteFile(filepath.Join(a.Home, ".gitconfig"), []byte(gitconfig), 0o640); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{"site/releases/initial/public", "logs", "tmp"} {
		run("/usr/bin/mkdir", "-p", filepath.Join(a.Home, dir))
	}
	run("/usr/bin/ln", "-s", "releases/initial", filepath.Join(a.Home, "site/current"))
	run("/usr/bin/chown", "-R", user+":"+user, a.Home)

	// go test keeps its binary in a 0700 directory, out of the user's reach.
	dir, err := os.MkdirTemp("", "zimam-deploy-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "deploy.test")
	copyExecutable(t, bin)
	h := &helper.Client{AgentBinary: bin}

	p := paths.Paths{Root: t.TempDir()}
	st := sites.State{Name: deploySite, Domain: "deploytest.example.com", Type: contract.SiteStatic, TemplateVersion: 1, Security: sites.DefaultSecurity(),
		Repository: "octo/app", Branch: "main", DeployKeyFingerprint: testRepoFP}
	if err := sites.Save(p, st); err != nil {
		t.Fatal(err)
	}
	d := sysexec.Deps{Paths: p, Run: sysexec.NewFakeRunner(), Config: config.New("panel.example.com", "8.4", "8.5"), Logf: t.Logf, Now: time.Now}
	lookup := func(string) (helper.Account, error) { return a, nil }
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	res, err := Run(ctx, d, h, lookup, contract.SiteDeployParams{Name: deploySite, Trigger: contract.DeployManual,
		Script: "sleep 600 &\necho started in $(pwd)\n"})
	if err != nil {
		t.Fatal(err)
	}
	if left := processesOf(t, user); left != "" {
		t.Errorf("processes of %s are left after the deploy:\n%s", user, left)
	}
	if res.Commit != sha {
		t.Errorf("result %+v, want commit %s", res, sha)
	}
	rel := filepath.Join(a.Home, "site/releases", string(res.Release))
	if target, err := os.Readlink(filepath.Join(a.Home, "site/current")); err != nil || target != "releases/"+string(res.Release) {
		t.Errorf("current -> %q, %v", target, err)
	}
	for name, want := range map[string]os.FileMode{"public/index.html": 0o640, "bin/run.sh": 0o750} {
		fi, err := os.Lstat(filepath.Join(rel, name))
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode() != want {
			t.Errorf("%s mode %v, want %v", name, fi.Mode(), want)
		}
	}
	out, err := exec.Command("/usr/bin/stat", "-c", "%U", filepath.Join(rel, "public/index.html"), filepath.Join(a.Home, MirrorDir)).Output()
	if err != nil || strings.Count(string(out), user) != 2 {
		t.Errorf("owners %q (%v), want %s", out, err, user)
	}
}

// processesOf lists user's processes, "" when there are none. pgrep exits 1
// for none; any other failure fails the test, so a missing pgrep can't pass
// for an empty list. A killed process may stay a zombie for a moment, so it
// looks again for up to 3 s.
func processesOf(t *testing.T, user string) string {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		out, err := exec.Command("/usr/bin/pgrep", "-u", user, "-a").Output()
		var exit *exec.ExitError
		switch {
		case errors.As(err, &exit) && exit.ExitCode() == 1:
			return ""
		case err != nil:
			t.Fatalf("pgrep -u %s: %v", user, err)
		case time.Now().After(deadline):
			return string(out)
		}
		time.Sleep(100 * time.Millisecond)
	}
}
