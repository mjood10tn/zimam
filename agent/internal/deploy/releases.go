package deploy

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"strings"
	"time"

	"zimam/agent/contract"
	"zimam/agent/internal/helper"
	"zimam/agent/internal/php"
	"zimam/agent/internal/sites"
	"zimam/agent/internal/sysexec"
)

// releaseID names a release: <UTC yyyymmddhhmmss>-<first 7 hex of the
// commit> ("Data the agent keeps").
func releaseID(t time.Time, sha contract.CommitSHA) contract.ReleaseID {
	return contract.ReleaseID(t.UTC().Format("20060102150405") + "-" + string(sha[:7]))
}

// keptReleases is the set of release directory names a prune keeps: the
// newest keep releases the state lists, the live one and the one before it
// (which may be "initial", or an old release after a rollback). Anything
// else under site/releases goes, half-built releases the state never
// listed included.
func keptReleases(listed []contract.Release, keep contract.KeepReleases, live, previous string) map[string]bool {
	ids := make([]string, 0, len(listed))
	for _, r := range listed {
		ids = append(ids, string(r.ID))
	}
	// An id starts with its time, so the newest sort last.
	slices.SortFunc(ids, func(a, b string) int { return strings.Compare(b, a) })
	kept := map[string]bool{}
	for _, id := range ids[:min(len(ids), int(keep))] {
		kept[id] = true
	}
	for _, name := range []string{live, previous} {
		if name != "" {
			kept[name] = true
		}
	}
	return kept
}

// keepListed is the state's releases that the prune keeps, in their order.
func keepListed(listed []contract.Release, kept map[string]bool) []contract.Release {
	out := []contract.Release{}
	for _, r := range listed {
		if kept[string(r.ID)] {
			out = append(out, r)
		}
	}
	return out
}

// prune is step 11: everything under site/releases but the kept names.
func prune(ctx context.Context, d sysexec.Deps, h DeployHelper, a helper.Account, keep contract.KeepReleases, kept map[string]bool) error {
	d.Logf("Pruning releases beyond the %d kept, besides the live one and the one before it", keep)
	l, err := h.List(ctx, a, releasesDir)
	if err != nil {
		return err
	}
	for _, e := range l.Entries {
		if kept[e.Name] {
			continue
		}
		d.Logf("Removing release %s", e.Name)
		if err := h.RemoveTree(ctx, a, releasesDir+"/"+e.Name); err != nil {
			return err
		}
	}
	return nil
}

// RollbackHelper is what a rollback needs of the site-user helper.
type RollbackHelper interface {
	swapper
}

// ReleasesHelper is what site.releases needs of the site-user helper.
type ReleasesHelper interface {
	statter
	List(ctx context.Context, a helper.Account, dir string) (helper.Listing, error)
}

var (
	_ RollbackHelper = (*helper.Client)(nil)
	_ ReleasesHelper = (*helper.Client)(nil)
)

// CheckRollback is site.rollback's precheck: the site must exist
// (not_found), have a repository (conflict, as for a deploy), and keep the
// release in its state (not_found).
func CheckRollback(d sysexec.Deps, p contract.SiteRollbackParams) error {
	_, err := rollbackable(d, p)
	return err
}

func rollbackable(d sysexec.Deps, p contract.SiteRollbackParams) (sites.State, error) {
	st, err := loadSite(d, p.Name)
	if err != nil {
		return sites.State{}, err
	}
	if st.Repository == "" {
		return sites.State{}, &contract.AgentError{Code: contract.ErrConflict, Message: fmt.Sprintf("Site %s has no repository, so it has no releases to roll back to.", p.Name)}
	}
	if !slices.ContainsFunc(st.Releases, func(r contract.Release) bool { return r.ID == p.Release }) {
		return sites.State{}, &contract.AgentError{Code: contract.ErrNotFound, Message: fmt.Sprintf("Release %s isn't one site %s keeps.", p.Release, p.Name)}
	}
	return st, nil
}

// Rollback is site.rollback: it makes a kept release live again. The
// release must be in the state file and its directory in site/releases; an
// id the state doesn't list (never deployed, pruned, or a directory a
// deploy left behind) is not_found, and so is a listed one whose directory
// is gone or isn't a directory any more.
//
// It only swaps current and records the release as the active one: no
// release is built, removed or changed, keep_releases stays, and the next
// deploy's prune keeps this release as the one before it. A PHP site's
// version is marked for maintenance's graceful reload, as a deploy marks it
// (spike amendment A3). Rolling back to the release current already points
// at swaps nothing and succeeds, so a retried request is harmless.
//
// A swap that reports an error is read back as a deploy's is
// (swapCurrent): a release it made live is recorded, and the error says it
// is live.
func Rollback(ctx context.Context, d sysexec.Deps, h RollbackHelper, lookup sites.Lookup, p contract.SiteRollbackParams) (contract.SiteRollbackResult, error) {
	if err := p.Validate(); err != nil {
		return contract.SiteRollbackResult{}, err
	}
	st, err := rollbackable(d, p)
	if err != nil {
		return contract.SiteRollbackResult{}, err
	}
	a, err := lookup("s_" + string(p.Name))
	if err != nil {
		return contract.SiteRollbackResult{}, err
	}
	id := p.Release
	fi, err := h.Stat(ctx, a, releasesDir+"/"+string(id))
	switch {
	case errors.Is(err, fs.ErrNotExist) || err == nil && !fi.Mode.IsDir():
		return contract.SiteRollbackResult{}, &contract.AgentError{Code: contract.ErrNotFound,
			Message: fmt.Sprintf("Release %s of site %s is gone from ~/%s.", id, p.Name, releasesDir)}
	case err != nil:
		return contract.SiteRollbackResult{}, err
	}
	res := contract.SiteRollbackResult{Name: p.Name, Release: id}
	previous, err := liveRelease(ctx, h, a)
	if err != nil {
		return contract.SiteRollbackResult{}, err
	}
	if previous == string(id) {
		d.Logf("Release %s is already live; nothing to roll back", id)
		if !activeIs(st.Releases, id) {
			setActive(st.Releases, id)
			if err := sites.Save(d.Paths, st); err != nil {
				return res, fmt.Errorf("release %s is live, but recording it as the active one failed: %w", id, err)
			}
		}
		return res, nil
	}

	d.Logf("Making %s live again; current pointed at %s", id, previous)
	var after []error
	switch live, settled, err := swapCurrent(ctx, h, a, string(id), previous); {
	case err == nil:
	case live:
		d.Logf("Swapping current reported an error, but current points at %s: it is live", id)
		after = append(after, fmt.Errorf("swapping current reported: %w", err))
	case settled:
		return contract.SiteRollbackResult{}, err
	default:
		d.Logf("Where current points is unknown after the failed swap")
		return contract.SiteRollbackResult{}, err
	}
	// From here on the release is live; a failure says so.
	setActive(st.Releases, id)
	if err := sites.Save(d.Paths, st); err != nil {
		return res, liveBut(id, append(after, fmt.Errorf("recording it failed: %w", err)))
	}
	if st.Type.RunsPHP() {
		d.Logf("Marking PHP %s's pools for a graceful reload by the next maintenance run", st.PHPVersion)
		if err := php.MarkReload(d.Paths, st.PHPVersion); err != nil {
			after = append(after, fmt.Errorf("marking PHP %s for a reload failed: %w", st.PHPVersion, err))
		}
	}
	if len(after) > 0 {
		return res, liveBut(id, after)
	}
	d.Logf("Site %s serves %s again", p.Name, id)
	return res, nil
}

// activeIs reports whether id is the state's only active release.
func activeIs(rs []contract.Release, id contract.ReleaseID) bool {
	for _, r := range rs {
		if r.Active != (r.ID == id) {
			return false
		}
	}
	return true
}

func setActive(rs []contract.Release, id contract.ReleaseID) {
	for i := range rs {
		rs[i].Active = rs[i].ID == id
	}
}

// Releases is site.releases: the releases the state file keeps,
// cross-checked with site/releases. A listed release is offered only when
// its directory is there; one without is reported as missing. A release
// directory the state doesn't list is reported as unlisted, never offered.
// The placeholder "initial" and names that aren't release ids are neither.
// Active marks the release current points at, which is the truth when the
// state's flag disagrees (a save that failed after a swap). A site without
// a repository has no releases; that is no error.
func Releases(ctx context.Context, d sysexec.Deps, h ReleasesHelper, lookup sites.Lookup, name contract.SiteName) (contract.SiteReleasesResult, error) {
	res := contract.NewSiteReleasesResult(name)
	st, err := loadSite(d, name)
	if err != nil {
		return res, err
	}
	a, err := lookup("s_" + string(name))
	if err != nil {
		return res, err
	}
	live, err := liveRelease(ctx, h, a)
	if err != nil {
		return res, err
	}
	l, err := h.List(ctx, a, releasesDir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return res, err
	}
	dirs := map[string]bool{}
	for _, e := range l.Entries {
		dirs[e.Name] = e.Mode.IsDir()
	}
	listed := map[contract.ReleaseID]bool{}
	for _, r := range st.Releases {
		listed[r.ID] = true
		isDir, seen := dirs[string(r.ID)]
		if !seen && l.Truncated {
			// A listing cut short says nothing of what it left out.
			fi, err := h.Stat(ctx, a, releasesDir+"/"+string(r.ID))
			if err != nil && !errors.Is(err, fs.ErrNotExist) {
				return res, err
			}
			isDir = err == nil && fi.Mode.IsDir()
		}
		if !isDir {
			res.Missing = append(res.Missing, r.ID)
			continue
		}
		r.Active = string(r.ID) == live
		res.Releases = append(res.Releases, r)
	}
	for n, isDir := range dirs {
		if id := contract.ReleaseID(n); isDir && !listed[id] && id.Validate() == nil {
			res.Unlisted = append(res.Unlisted, id)
		}
	}
	// An id starts with its time, so the newest sort first this way.
	newest := func(a, b contract.ReleaseID) int { return strings.Compare(string(b), string(a)) }
	slices.SortFunc(res.Releases, func(a, b contract.Release) int { return newest(a.ID, b.ID) })
	slices.SortFunc(res.Missing, newest)
	slices.SortFunc(res.Unlisted, newest)
	return res, nil
}
