package deploy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"zimam/agent/contract"
	"zimam/agent/internal/paths"
	"zimam/agent/internal/sites"
	"zimam/agent/internal/sysexec"
)

// site.rollback and site.releases, against releases real deploys made.

func (e *deployEnv) rollback(id contract.ReleaseID) (contract.SiteRollbackResult, error) {
	e.t.Helper()
	return Rollback(context.Background(), e.d, e.h, e.lookup, contract.SiteRollbackParams{Name: "shop", Release: id})
}

func (e *deployEnv) listReleases() contract.SiteReleasesResult {
	e.t.Helper()
	res, err := Releases(context.Background(), e.d, e.h, e.lookup, "shop")
	if err != nil {
		e.t.Fatalf("releases: %v", err)
	}
	return res
}

// deployN deploys n commits whose index.html is their number.
func (e *deployEnv) deployN(n int) []contract.ReleaseID {
	e.t.Helper()
	var ids []contract.ReleaseID
	for i := range n {
		e.up.commit(map[string]string{"index.html": fmt.Sprint(i)})
		ids = append(ids, e.mustDeploy(contract.SiteDeployParams{}).Release)
	}
	return ids
}

// tree is every path under site/releases, with its link target or size, so
// a test can tell that a rollback changed nothing there.
func (e *deployEnv) tree() []string {
	e.t.Helper()
	var out []string
	root := e.home("site/releases")
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		fi, err := d.Info()
		if err != nil {
			return err
		}
		target, _ := os.Readlink(p)
		rel, _ := filepath.Rel(root, p)
		out = append(out, fmt.Sprintf("%s %v %s %d", rel, fi.Mode(), target, fi.Size()))
		return nil
	})
	if err != nil {
		e.t.Fatal(err)
	}
	return out
}

func wantAgentCode(t *testing.T, err error, code contract.ErrorCode) {
	t.Helper()
	var ae *contract.AgentError
	if !errors.As(err, &ae) || ae.Code != code {
		t.Fatalf("err = %v, want %s", err, code)
	}
}

func activeIDs(rs []contract.Release) []contract.ReleaseID {
	var out []contract.ReleaseID
	for _, r := range rs {
		if r.Active {
			out = append(out, r.ID)
		}
	}
	return out
}

func ids(rs []contract.Release) []contract.ReleaseID {
	var out []contract.ReleaseID
	for _, r := range rs {
		out = append(out, r.ID)
	}
	return out
}

// TestRollbackSwapsToKeptRelease: current points at the kept release again,
// the state marks it active and keeps everything else, and nothing under
// site/releases changes: a rollback only swaps current.
func TestRollbackSwapsToKeptRelease(t *testing.T) {
	e := newDeployEnv(t, contract.SiteStatic)
	rel := e.deployN(2)
	before := e.state()
	tree := e.tree()
	from := len(e.h.ops)

	res, err := e.rollback(rel[0])
	wantNoErr(t, err)
	if res != (contract.SiteRollbackResult{Name: "shop", Release: rel[0]}) {
		t.Errorf("result %+v", res)
	}
	if e.current() != "releases/"+string(rel[0]) {
		t.Errorf("current -> %s", e.current())
	}
	if got := readFile(t, e.home("site/current/index.html")); got != "0" {
		t.Errorf("the site serves %q", got)
	}
	if ops := e.opsFrom(from); !slices.Equal(ops, []string{"symlink-swap site/current releases/" + string(rel[0])}) {
		t.Errorf("operations %q; want only the swap", ops)
	}
	if got := e.tree(); !slices.Equal(got, tree) {
		t.Errorf("site/releases changed:\n%q\nwas\n%q", got, tree)
	}
	st := e.state()
	if !slices.Equal(ids(st.Releases), ids(before.Releases)) || !slices.Equal(activeIDs(st.Releases), []contract.ReleaseID{rel[0]}) || st.KeepReleases != before.KeepReleases {
		t.Errorf("state releases %+v, keep %d", st.Releases, st.KeepReleases)
	}
	if calls := e.d.Run.(*sysexec.FakeRunner).Calls(); len(calls) != 0 {
		t.Errorf("the rollback ran %v as root", calls)
	}
	// And forward again.
	if _, err := e.rollback(rel[1]); err != nil || e.current() != "releases/"+string(rel[1]) {
		t.Errorf("rollback to %s: %v, current -> %s", rel[1], err, e.current())
	}
}

// TestRollbackMarksPHPVersion (spike amendment A3): as a deploy does, a PHP
// site's rollback marks its version for maintenance's graceful reload and
// reloads nothing itself.
func TestRollbackMarksPHPVersion(t *testing.T) {
	e := newDeployEnv(t, contract.SitePHP)
	rel := e.deployN(2)
	wantNoErr(t, os.RemoveAll(e.p.FPMReloadDir()))
	if _, err := e.rollback(rel[0]); err != nil {
		t.Fatal(err)
	}
	checkModeOf(t, e.p.FPMReloadMark("8.4"), 0o600)
	if calls := e.d.Run.(*sysexec.FakeRunner).Calls(); len(calls) != 0 {
		t.Errorf("the rollback ran %v as root", calls)
	}
}

// TestRollbackUnknownIsNotFound: only a release the state keeps and whose
// directory is there can go live. An id the state never had, one a prune
// removed, a directory the state doesn't list (a half-built release) and a
// release replaced by a symlink are all refused, and nothing changes.
func TestRollbackUnknownIsNotFound(t *testing.T) {
	e := newDeployEnv(t, contract.SiteStatic)
	rel := e.deployN(2)
	gone := rel[0]
	wantNoErr(t, os.RemoveAll(e.home("site/releases/"+string(gone))))
	linked := e.deployN(1)[0]
	unlisted := contract.ReleaseID("20261115110000-abcdef0")
	wantNoErr(t, os.MkdirAll(e.home("site/releases/"+string(unlisted)+"/public"), 0o750))
	wantNoErr(t, os.RemoveAll(e.home("site/releases/"+string(linked))))
	wantNoErr(t, os.Symlink(string(rel[1]), e.home("site/releases/"+string(linked))))
	wantNoErr(t, os.Remove(e.home("site/current")))
	wantNoErr(t, os.Symlink("releases/"+string(rel[1]), e.home("site/current")))

	state := stateBytes(t, e.p)
	for _, id := range []contract.ReleaseID{"20990101000000-0000000", unlisted, gone, linked} {
		from := len(e.h.ops)
		_, err := e.rollback(id)
		wantAgentCode(t, err, contract.ErrNotFound)
		if ops := e.opsFrom(from); len(ops) != 0 {
			t.Errorf("rollback to %s did %q", id, ops)
		}
		if e.current() != "releases/"+string(rel[1]) || stateBytes(t, e.p) != state {
			t.Errorf("rollback to %s changed current (%s) or the state", id, e.current())
		}
	}
	// The precheck already refuses what the state doesn't list.
	wantAgentCode(t, CheckRollback(e.d, contract.SiteRollbackParams{Name: "shop", Release: unlisted}), contract.ErrNotFound)
	wantNoErr(t, CheckRollback(e.d, contract.SiteRollbackParams{Name: "shop", Release: rel[1]}))
}

// TestRollbackRefusesPathLikeID: the release is a release id and nothing
// else; "initial", dot names and paths are a validation failure before
// anything is touched.
func TestRollbackRefusesPathLikeID(t *testing.T) {
	e := newDeployEnv(t, contract.SiteStatic)
	e.deployN(1)
	for _, id := range []contract.ReleaseID{"", "initial", ".", "..", "../initial", "releases/initial",
		"20261115120000-abcdef0/..", "20261115120000-abcdef0/../../..", "/etc", "20261115120000-ABCDEF0",
		"20261115120000-abcdef0\n", "20261115120000-abcdef"} {
		p := contract.SiteRollbackParams{Name: "shop", Release: id}
		var ae *contract.AgentError
		if err := p.Validate(); !errors.As(err, &ae) || ae.Code != contract.ErrValidationFailed || ae.Fields["release"] == "" {
			t.Errorf("Validate(%q) = %v; want a release field error", id, err)
		}
		from := len(e.h.ops)
		_, err := e.rollback(id)
		wantAgentCode(t, err, contract.ErrValidationFailed)
		if len(e.h.ops) != from {
			t.Errorf("rollback to %q touched the home: %q", id, e.h.ops[from:])
		}
	}
}

// TestRollbackToLiveReleaseIsNoOp: rolling back to what current already
// points at succeeds without a swap, so a retried request is harmless. A
// state whose active flag disagrees with current is put right.
func TestRollbackToLiveReleaseIsNoOp(t *testing.T) {
	e := newDeployEnv(t, contract.SiteStatic)
	rel := e.deployN(2)
	state := stateBytes(t, e.p)
	from := len(e.h.ops)
	res, err := e.rollback(rel[1])
	wantNoErr(t, err)
	if res.Release != rel[1] || len(e.opsFrom(from)) != 0 || stateBytes(t, e.p) != state {
		t.Errorf("result %+v, operations %q", res, e.opsFrom(from))
	}

	st := e.state()
	st.Releases[0].Active, st.Releases[1].Active = true, false
	wantNoErr(t, sites.Save(e.p, st))
	from = len(e.h.ops)
	wantNoErr(t, func() error { _, err := e.rollback(rel[1]); return err }())
	if ops := e.opsFrom(from); len(ops) != 0 {
		t.Errorf("operations %q", ops)
	}
	if got := activeIDs(e.state().Releases); !slices.Equal(got, []contract.ReleaseID{rel[1]}) {
		t.Errorf("active %q, want %s", got, rel[1])
	}
}

// TestRollbackWithoutRepoIsConflict: as for a deploy, a site that never
// had site.repo has nothing to roll back to; a missing site is not_found.
func TestRollbackWithoutRepoIsConflict(t *testing.T) {
	p := paths.Paths{Root: t.TempDir()}
	d := sysexec.Deps{Paths: p, Run: sysexec.NewFakeRunner(), Logf: t.Logf}
	params := contract.SiteRollbackParams{Name: "shop", Release: "20261115120000-abcdef0"}
	wantAgentCode(t, CheckRollback(d, params), contract.ErrNotFound)
	wantNoErr(t, sites.Save(p, sites.State{Name: "shop", Domain: "shop.example.com", Type: contract.SiteStatic, TemplateVersion: 1, Security: sites.DefaultSecurity()}))
	wantAgentCode(t, CheckRollback(d, params), contract.ErrConflict)
	h := &localHelper{t: t}
	_, err := Rollback(context.Background(), d, h, nil, params)
	wantAgentCode(t, err, contract.ErrConflict)
	if len(h.ops) != 0 {
		t.Errorf("Rollback touched the home: %q", h.ops)
	}
}

// TestRollbackSwapErrorAfterRename (the deploy's C1 rule): a swap that
// reports an error after its rename landed has made the release live; the
// state records it, and the error says it is live.
func TestRollbackSwapErrorAfterRename(t *testing.T) {
	e := newDeployEnv(t, contract.SiteStatic)
	rel := e.deployN(2)
	tree := e.tree()
	e.h.swapThenFail = map[string]error{"site/current": errors.New("syncing site/: input/output error")}
	res, err := e.rollback(rel[0])
	if err == nil || !strings.Contains(err.Error(), "is live, but") || !strings.Contains(err.Error(), "input/output error") {
		t.Fatalf("rollback = %v; want the swap's error, with the release live", err)
	}
	if res.Release != rel[0] || e.current() != "releases/"+string(rel[0]) {
		t.Errorf("result %+v, current -> %s", res, e.current())
	}
	if got := activeIDs(e.state().Releases); !slices.Equal(got, []contract.ReleaseID{rel[0]}) {
		t.Errorf("active %q", got)
	}
	if !slices.Equal(e.tree(), tree) {
		t.Error("site/releases changed")
	}
}

// A swap that failed before its rename leaves current and the state alone.
func TestRollbackSwapFailure(t *testing.T) {
	e := newDeployEnv(t, contract.SiteStatic)
	rel := e.deployN(2)
	tree, state := e.tree(), stateBytes(t, e.p)
	e.h.failSwap = map[string]error{"site/current": errors.New("rename: no space left on device")}
	if _, err := e.rollback(rel[0]); err == nil || strings.Contains(err.Error(), "is live") {
		t.Fatalf("rollback = %v; want a failure with nothing changed", err)
	}
	if e.current() != "releases/"+string(rel[1]) || stateBytes(t, e.p) != state || !slices.Equal(e.tree(), tree) {
		t.Errorf("current -> %s, or the state or releases changed", e.current())
	}
}

// TestReleasesMarksActive: the kept releases, newest first, with the one
// current points at active. current is the truth: a state whose flag
// disagrees (a save that failed after a swap) doesn't move the mark.
func TestReleasesMarksActive(t *testing.T) {
	e := newDeployEnv(t, contract.SiteStatic)
	rel := e.deployN(3)
	if _, err := e.rollback(rel[1]); err != nil {
		t.Fatal(err)
	}
	res := e.listReleases()
	if res.Name != "shop" || !slices.Equal(ids(res.Releases), []contract.ReleaseID{rel[2], rel[1], rel[0]}) ||
		!slices.Equal(activeIDs(res.Releases), []contract.ReleaseID{rel[1]}) || len(res.Missing) != 0 || len(res.Unlisted) != 0 {
		t.Errorf("releases %+v", res)
	}
	for _, r := range res.Releases {
		if r.Commit.Validate() != nil || r.Created.IsZero() {
			t.Errorf("release %+v", r)
		}
	}

	st := e.state()
	for i := range st.Releases {
		st.Releases[i].Active = st.Releases[i].ID == rel[0]
	}
	wantNoErr(t, sites.Save(e.p, st))
	if got := activeIDs(e.listReleases().Releases); !slices.Equal(got, []contract.ReleaseID{rel[1]}) {
		t.Errorf("active %q, want %s, where current points", got, rel[1])
	}
	// current on the placeholder: nothing is active.
	wantNoErr(t, os.Remove(e.home("site/current")))
	wantNoErr(t, os.Symlink(initialRel, e.home("site/current")))
	if got := activeIDs(e.listReleases().Releases); len(got) != 0 {
		t.Errorf("active %q, want none", got)
	}
}

// TestReleasesReportsMissingDirectory: a release the state lists whose
// directory is gone (or isn't a directory) is reported as missing, not
// offered; a release directory the state doesn't list is reported as
// unlisted. The placeholder and names that aren't release ids are neither.
func TestReleasesReportsMissingDirectory(t *testing.T) {
	e := newDeployEnv(t, contract.SiteStatic)
	rel := e.deployN(3)
	wantNoErr(t, os.RemoveAll(e.home("site/releases/"+string(rel[0]))))
	wantNoErr(t, os.RemoveAll(e.home("site/releases/"+string(rel[1]))))
	wantNoErr(t, os.Symlink(string(rel[2]), e.home("site/releases/"+string(rel[1]))))
	unlisted := contract.ReleaseID("20261115110000-abcdef0")
	for _, dir := range []string{string(unlisted), "initial", "junk", ".hidden"} {
		wantNoErr(t, os.MkdirAll(e.home("site/releases/"+dir), 0o750))
	}
	wantNoErr(t, os.WriteFile(e.home("site/releases/20261115100000-1234567"), []byte("x"), 0o640))

	res := e.listReleases()
	if !slices.Equal(ids(res.Releases), []contract.ReleaseID{rel[2]}) || !res.Releases[0].Active {
		t.Errorf("releases %+v", res.Releases)
	}
	if !slices.Equal(res.Missing, []contract.ReleaseID{rel[1], rel[0]}) {
		t.Errorf("missing %q, want %q", res.Missing, []contract.ReleaseID{rel[1], rel[0]})
	}
	if !slices.Equal(res.Unlisted, []contract.ReleaseID{unlisted}) {
		t.Errorf("unlisted %q, want %s", res.Unlisted, unlisted)
	}
}

// A site without deploys lists three empty arrays, never null; one without
// a repository too, and a missing site is not_found.
func TestReleasesEmpty(t *testing.T) {
	e := newDeployEnv(t, contract.SiteStatic)
	b, err := json.Marshal(e.listReleases())
	if want := `{"name":"shop","releases":[],"missing":[],"unlisted":[]}`; err != nil || string(b) != want {
		t.Errorf("site.releases = %s, %v; want %s", b, err, want)
	}
	st := e.state()
	st.Repository, st.Branch, st.DeployKeyFingerprint = "", "", ""
	wantNoErr(t, sites.Save(e.p, st))
	wantNoErr(t, os.RemoveAll(e.home("site/releases")))
	b, err = json.Marshal(e.listReleases())
	if want := `{"name":"shop","releases":[],"missing":[],"unlisted":[]}`; err != nil || string(b) != want {
		t.Errorf("site.releases without a repository = %s, %v; want %s", b, err, want)
	}
	_, err = Releases(context.Background(), e.d, e.h, e.lookup, "other")
	wantAgentCode(t, err, contract.ErrNotFound)
}
