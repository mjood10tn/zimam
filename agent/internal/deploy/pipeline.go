package deploy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"strings"
	"time"

	"zimam/agent/contract"
	"zimam/agent/internal/composer"
	"zimam/agent/internal/helper"
	"zimam/agent/internal/php"
	"zimam/agent/internal/siteexec"
	"zimam/agent/internal/sites"
	"zimam/agent/internal/sysexec"
)

// Paths inside a site's home, relative to it (Site layout).
const (
	releasesDir = "site/releases"
	currentLink = "site/current"
	sharedDir   = "site/shared"
)

// DeployHelper is what a deploy needs of the site-user helper;
// *helper.Client is the real one.
type DeployHelper interface {
	Helper
	List(ctx context.Context, a helper.Account, dir string) (helper.Listing, error)
	SymlinkSwap(ctx context.Context, a helper.Account, link, target string) error
	ExtractTar(ctx context.Context, a helper.Account, dest string, tarball io.Reader, lim helper.ExtractLimits) error
	Chmod(ctx context.Context, a helper.Account, name string, mode fs.FileMode) error
}

var _ DeployHelper = (*helper.Client)(nil)

// Limits on one release's export: what extract-tar unpacks, as for the
// panel's own release, and the compressed archive git may stream to it.
var extractLimits = helper.ExtractLimits{MaxBytes: 1 << 30, MaxEntries: 200_000}

const (
	maxArchiveBytes = 1 << 30
	maxRevParse     = 128 // a commit hash and its newline
	// cleanupTimeout bounds removing a failed release, which runs even
	// when the job was cancelled.
	cleanupTimeout = 5 * time.Minute
)

// ensureComposer is composer.Ensure; tests replace it.
var ensureComposer = composer.Ensure

// composerArgs is Composer's command line (R02-D5 step 6).
var composerArgs = []string{"install", "--no-dev", "--no-interaction", "--prefer-dist", "--optimize-autoloader", "--no-progress"}

// CheckDeploy is site.deploy's precheck: the site must exist (not_found)
// and have a repository (conflict).
func CheckDeploy(d sysexec.Deps, p contract.SiteDeployParams) error {
	_, err := deployable(d, p.Name)
	return err
}

func deployable(d sysexec.Deps, name contract.SiteName) (sites.State, error) {
	st, err := loadSite(d, name)
	if err != nil {
		return sites.State{}, err
	}
	if st.Repository == "" {
		return sites.State{}, &contract.AgentError{Code: contract.ErrConflict, Message: fmt.Sprintf("Site %s has no repository to deploy from; connect one first.", name)}
	}
	return st, nil
}

// Run is site.deploy, the release pipeline of R02-D5 as the spike amended
// it (A1-A3). The job's subject already holds the site's lock (step 1).
//
//  2. fetch the branch into site/repo.git, made on the first deploy as an
//     empty bare repository with GitHub as origin (A2);
//  3. resolve the commit: the branch's head, or the given one, which the
//     branch must reach;
//  4. stream git archive of the commit's tree (A1) into extract-tar, into
//     site/releases/<id>;
//  5. link shared/.env and shared/storage into the release, where the site
//     has them;
//  6. Composer, when it is on and the release has a composer.json;
//  7. the deploy script, from stdin, with the release as its directory;
//  8. bootstrap/cache 0700 and its files 0600;
//  9. swap current to the release, and record it in the state file;
//  10. mark the site's PHP version for maintenance's graceful reload (A3);
//  11. prune releases beyond keep_releases, never the live one or the one
//     before it.
//
// Every program runs as the site user through the helper. A failure in
// steps 4-8 removes the new release and leaves current alone; one after
// step 9 is reported with the new release live.
func Run(ctx context.Context, d sysexec.Deps, h DeployHelper, lookup sites.Lookup, p contract.SiteDeployParams) (contract.SiteDeployResult, error) {
	if err := p.Validate(); err != nil {
		return contract.SiteDeployResult{}, err
	}
	st, err := deployable(d, p.Name)
	if err != nil {
		return contract.SiteDeployResult{}, err
	}
	a, err := lookup("s_" + string(p.Name))
	if err != nil {
		return contract.SiteDeployResult{}, err
	}
	b := &build{ctx: ctx, d: d, h: h, a: a, st: st, p: p}
	sha, err := b.fetch()
	if err != nil {
		return contract.SiteDeployResult{}, err
	}
	now := time.Now()
	if d.Now != nil {
		now = d.Now()
	}
	id := releaseID(now, sha)
	rel := releasesDir + "/" + string(id)
	previous, err := liveRelease(ctx, h, a)
	if err != nil {
		return contract.SiteDeployResult{}, err
	}
	// The release must be new: only then may a failure remove it.
	switch taken, err := present(ctx, h, a, rel); {
	case err != nil:
		return contract.SiteDeployResult{}, err
	case taken || previous == string(id):
		return contract.SiteDeployResult{}, &contract.AgentError{Code: contract.ErrConflict,
			Message: fmt.Sprintf("Release %s already exists: this commit was deployed a moment ago. Deploy again in a second.", id)}
	}
	if err := b.build(rel, sha); err != nil {
		b.discard(rel, previous)
		return contract.SiteDeployResult{}, err
	}

	d.Logf("Making %s live", id)
	// after collects what fails once the release is live.
	var after []error
	// A release a failed swap made live stays, and is recorded.
	switch live, settled, err := swapCurrent(ctx, h, a, string(id), previous); {
	case err == nil:
	case live:
		d.Logf("Swapping current reported an error, but current points at %s: it is live", id)
		after = append(after, fmt.Errorf("swapping current reported: %w", err))
	case settled:
		b.discard(rel, previous)
		return contract.SiteDeployResult{}, err
	default:
		d.Logf("Where current points is unknown after the failed swap; ~/%s stays for the next deploy's prune", rel)
		return contract.SiteDeployResult{}, err
	}
	res := contract.SiteDeployResult{Name: p.Name, Release: id, Commit: sha}
	if prev := contract.ReleaseID(previous); prev.Validate() == nil {
		res.PreviousRelease = &prev
	}
	// From here on the new release is live; a failure says so.
	if p.KeepReleases != nil {
		st.KeepReleases = *p.KeepReleases
	}
	for i := range st.Releases {
		st.Releases[i].Active = false
	}
	st.Releases = append(st.Releases, contract.Release{ID: id, Commit: sha, Created: now.UTC(), Active: true})
	kept := keptReleases(st.Releases, st.Keep(), string(id), previous)
	st.Releases = keepListed(st.Releases, kept)
	if err := sites.Save(d.Paths, st); err != nil {
		// Without the record, a prune could remove what the state still lists.
		return res, liveBut(id, append(after, fmt.Errorf("recording it failed: %w", err)))
	}
	if st.Type.RunsPHP() {
		d.Logf("Marking PHP %s's pools for a graceful reload by the next maintenance run", st.PHPVersion)
		if err := php.MarkReload(d.Paths, st.PHPVersion); err != nil {
			after = append(after, fmt.Errorf("marking PHP %s for a reload failed: %w", st.PHPVersion, err))
		}
	}
	if err := prune(ctx, d, h, a, st.Keep(), kept); err != nil {
		after = append(after, fmt.Errorf("pruning old releases failed: %w", err))
	}
	if len(after) > 0 {
		return res, liveBut(id, after)
	}
	d.Logf("Site %s now serves %s at %s", p.Name, sha, id)
	return res, nil
}

// build is one deploy's work as the site user.
type build struct {
	ctx context.Context
	d   sysexec.Deps
	h   DeployHelper
	a   helper.Account
	st  sites.State
	p   contract.SiteDeployParams
}

// git runs git as the site user in dir, through the pinned SSH command.
func (b *build) git(d sysexec.Deps, dir string, capture io.Writer, max int64, args ...string) error {
	_, err := siteexec.Run(b.ctx, d, b.h, b.a, siteexec.Request{
		Program:    siteexec.Git,
		Args:       args,
		Dir:        dir,
		Env:        map[string]string{"GIT_SSH_COMMAND": gitSSHCommand(d.Config)},
		LogPrefix:  "git: ",
		Capture:    capture,
		CaptureMax: max,
	})
	return err
}

// watching is d with a Logf that also reports whether a line contained
// text, so a git failure can be told apart by what git said.
func watching(d sysexec.Deps, text string) (sysexec.Deps, *bool) {
	seen := new(bool)
	logf := d.Logf
	d.Logf = func(format string, args ...any) {
		line := fmt.Sprintf(format, args...)
		if strings.Contains(line, text) {
			*seen = true
		}
		logf("%s", line)
	}
	return d, seen
}

// fetch is steps 2 and 3: it returns the commit to deploy.
func (b *build) fetch() (contract.CommitSHA, error) {
	branch := b.st.Branch
	ref := "refs/heads/" + string(branch)
	if err := b.ensureRepo(ref); err != nil {
		return "", err
	}
	b.d.Logf("Fetching %s from %s", branch, b.st.Repository)
	// --refmap= ignores origin's configured refspec, so git updates only the
	// branch, never a refs/remotes/origin/ copy of it (A2). git says
	// "couldn't find remote ref" for a branch the repository lacks; that
	// becomes a clear error rather than git's.
	d, missing := watching(b.d, "couldn't find remote ref")
	if err := b.git(d, MirrorDir, nil, 0, "fetch", "--prune", "--no-tags", "--refmap=", "origin", "+"+ref+":"+ref); err != nil {
		if *missing {
			return "", &contract.AgentError{Code: contract.ErrNotFound, Message: fmt.Sprintf("Branch %s isn't in %s.", branch, b.st.Repository)}
		}
		return "", err
	}
	if b.p.Commit != nil {
		c := *b.p.Commit
		// Exit 1 is "not an ancestor"; 128 with this message is a commit
		// the branch's history doesn't hold. Anything else is git failing,
		// not the caller's mistake.
		d, unknown := watching(b.d, "Not a valid commit name")
		err := b.git(d, MirrorDir, nil, 0, "merge-base", "--is-ancestor", string(c), ref)
		var exit *sysexec.ExitError
		if errors.As(err, &exit) && (exit.Code == 1 || (exit.Code == 128 && *unknown)) {
			msg := fmt.Sprintf("Commit %s isn't on the branch %s.", c, branch)
			return "", &contract.AgentError{Code: contract.ErrValidationFailed, Message: msg, Fields: contract.FieldErrors{"commit": msg}}
		}
		if err != nil {
			return "", err
		}
		return c, nil
	}
	var out bytes.Buffer
	if err := b.git(b.d, MirrorDir, &out, maxRevParse, "rev-parse", "--verify", "--end-of-options", ref+"^{commit}"); err != nil {
		return "", err
	}
	sha := contract.CommitSHA(strings.TrimSuffix(out.String(), "\n"))
	if err := sha.Validate(); err != nil {
		return "", fmt.Errorf("git rev-parse printed %q: %w", out.String(), err)
	}
	return sha, nil
}

// ensureRepo makes site/repo.git on the first deploy: an empty bare
// repository with GitHub as origin, never a mirror clone (A2). A failure
// half way drops it, so the next deploy starts again. An existing one is
// repaired first (repairRepo).
func (b *build) ensureRepo(ref string) error {
	ok, err := present(b.ctx, b.h, b.a, MirrorDir)
	if err != nil {
		return err
	}
	if ok {
		return b.repairRepo(ref)
	}
	b.d.Logf("Making the site's repository ~/%s, with %s as origin", MirrorDir, b.st.Repository)
	err = b.git(b.d, "", nil, 0, "init", "--quiet", "--bare", MirrorDir)
	if err == nil {
		err = b.git(b.d, MirrorDir, nil, 0, "remote", "add", "origin", RemoteURL(b.st.Repository))
	}
	if err != nil {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(b.ctx), cleanupTimeout)
		defer cancel()
		if rerr := b.h.RemoveTree(ctx, b.a, MirrorDir); rerr != nil {
			b.d.Logf("Removing the half-made ~/%s failed too: %v", MirrorDir, rerr)
		}
		return err
	}
	return nil
}

// maxRemoteURL caps what git config prints for origin's URL.
const maxRemoteURL = 4 << 10

// repairRepo undoes what a killed or older run can leave in site/repo.git:
// the branch's lock file (or packed-refs') from a fetch killed half way,
// which is stale because the site's job lock keeps every other git of this
// repository out; and an origin that is missing or points elsewhere.
func (b *build) repairRepo(ref string) error {
	for _, lock := range []string{MirrorDir + "/" + ref + ".lock", MirrorDir + "/packed-refs.lock"} {
		stale, err := present(b.ctx, b.h, b.a, lock)
		if err != nil {
			return err
		}
		if stale {
			b.d.Logf("Removing ~/%s, left by a fetch that didn't finish", lock)
			if err := b.h.RemoveTree(b.ctx, b.a, lock); err != nil {
				return err
			}
		}
	}
	want := RemoteURL(b.st.Repository)
	var out bytes.Buffer
	err := b.git(b.d, MirrorDir, &out, maxRemoteURL, "config", "--get", "remote.origin.url")
	var exit *sysexec.ExitError
	switch {
	case errors.As(err, &exit) && exit.Code == 1: // no origin
		b.d.Logf("The site's repository has no origin; adding %s", want)
		return b.git(b.d, MirrorDir, nil, 0, "remote", "add", "origin", want)
	case err != nil:
		return err
	case strings.TrimSuffix(out.String(), "\n") != want:
		b.d.Logf("Pointing the site's repository's origin at %s", want)
		return b.git(b.d, MirrorDir, nil, 0, "remote", "set-url", "origin", want)
	}
	return nil
}

// liveRelease is the name of the release current points at, such as
// "initial" or a release id, or "" when current points elsewhere.
func liveRelease(ctx context.Context, h statter, a helper.Account) (string, error) {
	fi, err := h.Stat(ctx, a, currentLink)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	name, ok := strings.CutPrefix(fi.Target, "releases/")
	if fi.Mode&fs.ModeSymlink == 0 || !ok || name == "" || strings.ContainsAny(name, "/") || name == "." || name == ".." {
		return "", nil
	}
	return name, nil
}

// statter and swapper are the helper operations liveRelease and
// swapCurrent need.
type statter interface {
	Stat(ctx context.Context, a helper.Account, name string) (helper.FileInfo, error)
}

type swapper interface {
	statter
	SymlinkSwap(ctx context.Context, a helper.Account, link, target string) error
}

// swapCurrent points current at releases/<name>, for a deploy and a
// rollback alike. A swap can fail after its rename landed: a cancel seen
// late, or a failed sync of site/. Only current itself then says whether
// name is live, and a live release must never be removed or left out of
// the state, so on an error current is read back, even after a cancel:
//
//   - it points at name: live is true, with the swap's error;
//   - it still points at previous: settled is true, and nothing changed;
//   - otherwise, or when it can't be read, neither is: the error is the
//     swap's joined with the read's, and what is live is unknown.
func swapCurrent(ctx context.Context, h swapper, a helper.Account, name, previous string) (live, settled bool, err error) {
	err = h.SymlinkSwap(ctx, a, currentLink, "releases/"+name)
	if err == nil {
		return true, true, nil
	}
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
	defer cancel()
	now, serr := liveRelease(sctx, h, a)
	switch {
	case serr == nil && now == name:
		return true, true, err
	case serr == nil && now == previous:
		return false, true, err
	}
	return false, false, errors.Join(err, serr)
}

// build is steps 4 to 8 into rel.
func (b *build) build(rel string, sha contract.CommitSHA) error {
	if err := b.export(rel, sha); err != nil {
		return err
	}
	if err := b.linkShared(rel); err != nil {
		return err
	}
	if err := b.composer(rel); err != nil {
		return err
	}
	if err := b.script(rel); err != nil {
		return err
	}
	return b.tighten(rel)
}

// export is step 4: git archive of the commit's tree, which has no
// pax_global_header (A1), streamed byte for byte into extract-tar.
func (b *build) export(rel string, sha contract.CommitSHA) error {
	b.d.Logf("Exporting %s into ~/%s", sha, rel)
	pr, pw := io.Pipe()
	extracted := make(chan error, 1)
	go func() {
		err := b.h.ExtractTar(b.ctx, b.a, rel, pr, extractLimits)
		if err != nil {
			pr.CloseWithError(err) // git's next write fails, and the helper stops it
		} else {
			io.Copy(io.Discard, pr) // the archive's padding, so git ends well
		}
		extracted <- err
	}()
	gitErr := b.git(b.d, MirrorDir, pw, maxArchiveBytes, "archive", "--format=tar.gz", "--end-of-options", string(sha)+"^{tree}")
	pw.CloseWithError(gitErr) // nil: the end of the archive
	extractErr := <-extracted
	var exit *sysexec.ExitError
	switch {
	case errors.As(gitErr, &exit):
		return gitErr // git failed by itself; extract-tar only saw it stop
	case extractErr != nil && gitErr != nil:
		// Either may be the cause: extract-tar refusing a member stops git
		// (a failed capture write), and a helper that died ends the archive
		// early for extract-tar. Both are named.
		return fmt.Errorf("exporting %s: %w", sha, errors.Join(extractErr, gitErr))
	case extractErr != nil:
		return fmt.Errorf("exporting %s: %w", sha, extractErr)
	}
	return gitErr
}

// linkShared is step 5: the release's .env and storage become links to the
// site's shared ones, where the site has them. What the repository ships
// under those names gives way: a rename can't replace a directory.
func (b *build) linkShared(rel string) error {
	for _, name := range []string{".env", "storage"} {
		ok, err := present(b.ctx, b.h, b.a, sharedDir+"/"+name)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		b.d.Logf("Linking ~/%s/%s into the release", sharedDir, name)
		if err := b.h.RemoveTree(b.ctx, b.a, rel+"/"+name); err != nil {
			return err
		}
		if err := b.h.SymlinkSwap(b.ctx, b.a, rel+"/"+name, "../../shared/"+name); err != nil {
			return err
		}
	}
	return nil
}

// composer is step 6, with the site's PHP version, as the site user, with
// its Composer home and cache in the site's home (R02-D6).
func (b *build) composer(rel string) error {
	if !b.p.Composer {
		return nil
	}
	ok, err := present(b.ctx, b.h, b.a, rel+"/composer.json")
	if err != nil {
		return err
	}
	switch {
	case !ok:
		b.d.Logf("The release has no composer.json; skipping Composer")
		return nil
	case !b.st.Type.RunsPHP():
		b.d.Logf("Site %s is a static site without PHP; skipping Composer", b.st.Name)
		return nil
	}
	if _, err := ensureComposer(b.ctx, b.d); err != nil {
		return err
	}
	b.d.Logf("Running Composer with PHP %s", b.st.PHPVersion)
	_, err = siteexec.Run(b.ctx, b.d, b.h, b.a, siteexec.Request{
		Program: siteexec.Composer,
		PHP:     b.st.PHPVersion,
		Args:    composerArgs,
		Dir:     rel,
		Env: map[string]string{
			"COMPOSER_HOME":           b.a.Home + "/.composer",
			"COMPOSER_CACHE_DIR":      b.a.Home + "/.cache/composer",
			"COMPOSER_NO_INTERACTION": "1",
		},
		LogPrefix: "composer: ",
	})
	return err
}

// script is step 7: the owner's script on bash's stdin, never in argv
// (R02-D2), with the release as its directory.
func (b *build) script(rel string) error {
	if b.p.Script == "" {
		return nil
	}
	b.d.Logf("Running the deploy script")
	_, err := siteexec.Run(b.ctx, b.d, b.h, b.a, siteexec.Request{
		Program:   siteexec.DeployScript,
		Dir:       rel,
		Stdin:     strings.NewReader(string(b.p.Script)),
		LogPrefix: "deploy script: ",
	})
	return err
}

// tighten is step 8: bootstrap/cache 0700 and the files in it 0600, so a
// cached config's secrets stay out of www-data's reach. Only a real
// directory is touched, and only regular files in it.
func (b *build) tighten(rel string) error {
	dir := rel + "/bootstrap/cache"
	fi, err := b.h.Stat(b.ctx, b.a, dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !fi.Mode.IsDir() {
		return nil
	}
	b.d.Logf("Tightening bootstrap/cache: the directory 0700, its files 0600")
	if err := b.h.Chmod(b.ctx, b.a, dir, 0o700); err != nil {
		return err
	}
	l, err := b.h.List(b.ctx, b.a, dir)
	if err != nil {
		return err
	}
	for _, e := range l.Entries {
		if !e.Mode.IsRegular() {
			continue
		}
		if err := b.h.Chmod(b.ctx, b.a, path.Join(dir, e.Name), 0o600); err != nil {
			return err
		}
	}
	return nil
}

// discard removes a release that never went live. It runs even when the
// job was cancelled; if it fails, the next deploy's prune removes the
// release, which the state never listed. It never touches the live one.
func (b *build) discard(rel, live string) {
	if path.Base(rel) == live {
		return
	}
	b.d.Logf("Removing the unfinished release ~/%s; current stays on %s", rel, live)
	ctx, cancel := context.WithTimeout(context.WithoutCancel(b.ctx), cleanupTimeout)
	defer cancel()
	if err := b.h.RemoveTree(ctx, b.a, rel); err != nil {
		b.d.Logf("Removing ~/%s failed (%v); the next deploy removes it", rel, err)
	}
}

// liveBut reports failures after the swap: the deploy's release is live.
func liveBut(id contract.ReleaseID, errs []error) error {
	return fmt.Errorf("release %s is live, but %w", id, errors.Join(errs...))
}
