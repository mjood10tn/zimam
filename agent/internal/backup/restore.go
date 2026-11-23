package backup

import (
	"archive/tar"
	"bytes"
	"cmp"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"slices"

	"zimam/agent/contract"
	"zimam/agent/internal/helper"
	"zimam/agent/internal/mysql"
	"zimam/agent/internal/rclone"
	"zimam/agent/internal/sites"
	"zimam/agent/internal/sysexec"
)

// rcloneCat streams an rclone cat to w; tests replace it.
var rcloneCat = rclone.RunOutput

const mysqlPath = "/usr/bin/mysql"

// restoreLimits bound a files restore. The archive is the agent's own,
// its hash checked, so these only stop a runaway, not an attack.
var restoreLimits = helper.ExtractLimits{MaxBytes: 1 << 40, MaxEntries: 10_000_000}

// RestoreHelper is what a files restore asks of the helper (D5).
type RestoreHelper interface {
	Stat(ctx context.Context, a helper.Account, name string) (helper.FileInfo, error)
	Mkdir(ctx context.Context, a helper.Account, name string, mode fs.FileMode) error
	ExtractTar(ctx context.Context, a helper.Account, dest string, tarball io.Reader, lim helper.ExtractLimits) error
	RemoveTree(ctx context.Context, a helper.Account, name string) error
}

// RestoreEnv is what a restore needs besides Deps. A new database is made
// and dropped the way db.create and db.drop do it: CheckNew is db.create's
// check, run before anything is downloaded.
type RestoreEnv struct {
	Helper   RestoreHelper
	Lookup   func(username string) (helper.Account, error)
	CheckNew func(db contract.DatabaseName) error
	Create   func(ctx context.Context, db contract.DatabaseName) error
	Drop     func(ctx context.Context, db contract.DatabaseName) error
}

// planDestination is a plan's destination, or not_found.
func planDestination(p contract.PlanID, d sysexec.Deps) (contract.DestinationID, error) {
	plans, err := loadPlans(d.Paths)
	if err != nil {
		return "", err
	}
	i := findPlan(plans, p)
	if i < 0 {
		return "", planNotFound(p)
	}
	return plans[i].Destination, nil
}

// missing reports rclone's "directory not found" (3) and "file not found"
// (4) exits.
func missing(err error) bool {
	var exit *sysexec.ExitError
	return errors.As(err, &exit) && (exit.Code == 3 || exit.Code == 4)
}

// List answers backup.list: the plan's complete backups, oldest first, read
// from their manifests in one rclone cat. A backup without a manifest is
// incomplete and never listed (R05-D5).
func List(ctx context.Context, d sysexec.Deps, plan contract.PlanID) (contract.BackupListResult, error) {
	res := contract.BackupListResult{Backups: []contract.BackupInfo{}}
	dest, err := planDestination(plan, d)
	if err != nil {
		return res, err
	}
	var buf bytes.Buffer
	err = rcloneCat(ctx, d, &buf, "cat", "--include", "/*/"+manifestName, string(dest)+":"+string(plan))
	if missing(err) {
		return res, nil
	}
	if err != nil {
		return res, fmt.Errorf("reading the manifests of %s: %w", plan, err)
	}
	dec := json.NewDecoder(&buf)
	dec.DisallowUnknownFields()
	for {
		var m Manifest
		if err := dec.Decode(&m); err == io.EOF {
			break
		} else if err != nil {
			return res, fmt.Errorf("reading the manifests of %s: %w", plan, err)
		}
		if m.Plan == plan && m.Backup.Validate() == nil {
			res.Backups = append(res.Backups, m.BackupInfo)
		}
	}
	slices.SortFunc(res.Backups, func(a, b contract.BackupInfo) int { return cmp.Compare(a.Backup, b.Backup) })
	return res, nil
}

// Restore answers backup.restore (R05-D10). Everything that can refuse is
// checked first; then the part is downloaded and its hash checked against
// the manifest, and only then is anything written. A database part goes
// into a new database of the site, which a failure drops again, or with
// overwrite into one the site has. A files part goes into
// ~/restores/<backup>/ through the helper, never over the live site (OQ6).
func Restore(ctx context.Context, d sysexec.Deps, env RestoreEnv, p *contract.BackupRestoreParams) (contract.BackupRestoreResult, error) {
	res := contract.BackupRestoreResult{Site: p.Site, Part: p.Part}
	st, err := sites.Load(d.Paths, p.Site)
	if errors.Is(err, fs.ErrNotExist) {
		return res, &contract.AgentError{Code: contract.ErrNotFound, Message: fmt.Sprintf("site %s doesn't exist", p.Site)}
	}
	if err != nil {
		return res, err
	}
	dest, err := planDestination(p.Plan, d)
	if err != nil {
		return res, err
	}
	db, isDB := p.Part.Database()
	var acct helper.Account
	folder := "restores/" + string(p.Backup)
	if isDB {
		res.Database = p.Database
		if err := checkDatabaseRestore(d, env, st, db, p); err != nil {
			return res, err
		}
	} else {
		if acct, err = env.Lookup("s_" + string(p.Site)); err != nil {
			return res, err
		}
		switch _, err := env.Helper.Stat(ctx, acct, folder); {
		case err == nil:
			return res, conflict("~/%s already exists in site %s's home; move or delete it first", folder, p.Site)
		case !errors.Is(err, fs.ErrNotExist):
			return res, err
		}
	}
	spool, done, err := fetch(ctx, d, dest, p.Plan, p.Backup, p.Part)
	if err != nil {
		return res, err
	}
	defer done()
	if isDB {
		return res, restoreDatabase(ctx, d, env, spool, p)
	}
	res.Folder = acct.Home + "/" + folder
	res.SkippedLinks, err = restoreFiles(ctx, d, env.Helper, acct, spool, folder)
	return res, err
}

func conflict(format string, args ...any) error {
	return &contract.AgentError{Code: contract.ErrConflict, Message: fmt.Sprintf(format, args...)}
}

// checkDatabaseRestore refuses a database restore before anything is
// downloaded: the part must be this site's database, and the target a new
// name db.create accepts, or with overwrite one the site has.
func checkDatabaseRestore(d sysexec.Deps, env RestoreEnv, st sites.State, db contract.DatabaseName, p *contract.BackupRestoreParams) error {
	all, err := sites.All(d.Paths)
	if err != nil {
		return err
	}
	names := make([]contract.SiteName, len(all))
	for i, s := range all {
		names[i] = s.Name
	}
	switch owner, ok := db.OwnerAmong(names); {
	case !ok:
		return conflict("the part %s belongs to no site now; only a site's own databases are restored into it", p.Part)
	case owner != p.Site:
		return conflict("the part %s is site %s's database; restore it into a database of %s", p.Part, owner, owner)
	}
	switch {
	case p.Overwrite && !st.HasDatabase(p.Database):
		return &contract.AgentError{Code: contract.ErrNotFound, Message: fmt.Sprintf("site %s has no database %s to overwrite", p.Site, p.Database)}
	case p.Overwrite:
		ok, err := mysql.Installed(d)
		if err != nil {
			return err
		}
		if !ok {
			return conflict("MySQL isn't installed on this server")
		}
		return nil
	case st.HasDatabase(p.Database):
		return conflict("site %s already has the database %s; restore into a new name, or overwrite it", p.Site, p.Database)
	}
	return env.CheckNew(p.Database)
}

// fetch downloads a part of a complete backup into a root-only spool file
// and checks its size and SHA-256 against the manifest. done closes and
// removes the file.
func fetch(ctx context.Context, d sysexec.Deps, dest contract.DestinationID, plan contract.PlanID, id contract.BackupID, part contract.BackupPart) (*os.File, func(), error) {
	dir := backupDir(dest, plan, id)
	var buf bytes.Buffer
	err := rcloneCat(ctx, d, &buf, "cat", dir+manifestName)
	if missing(err) {
		return nil, nil, &contract.AgentError{Code: contract.ErrNotFound, Message: fmt.Sprintf("backup %s of %s doesn't exist or isn't complete", id, plan)}
	}
	if err != nil {
		return nil, nil, fmt.Errorf("reading the manifest of %s: %w", id, err)
	}
	var m Manifest
	if err := contract.DecodeStrict(buf.Bytes(), &m); err != nil {
		return nil, nil, fmt.Errorf("the manifest of %s: %w", id, err)
	}
	i := slices.IndexFunc(m.Parts, func(x contract.BackupPartInfo) bool { return x.Part == part })
	if m.Plan != plan || m.Backup != id || i < 0 {
		return nil, nil, &contract.AgentError{Code: contract.ErrNotFound, Message: fmt.Sprintf("backup %s of %s has no part %s", id, plan, part)}
	}
	want := m.Parts[i]
	if err := os.MkdirAll(d.Paths.RestoreSpoolDir(), 0o700); err != nil {
		return nil, nil, err
	}
	f, err := os.CreateTemp(d.Paths.RestoreSpoolDir(), "part-*")
	if err != nil {
		return nil, nil, err
	}
	done := func() {
		f.Close()
		os.Remove(f.Name())
	}
	sum := sha256.New()
	var n counter
	d.Logf("Downloading %s of backup %s", part, id)
	if err := rcloneCat(ctx, d, io.MultiWriter(f, sum, &n), "cat", dir+ObjectName(part)); err != nil {
		done()
		return nil, nil, fmt.Errorf("downloading %s: %w", part, err)
	}
	if got := hex.EncodeToString(sum.Sum(nil)); int64(n) != int64(want.Bytes) || got != want.SHA256 {
		done()
		return nil, nil, conflict("%s of backup %s doesn't match the size and SHA-256 its manifest records: it was changed or damaged on the destination; nothing was restored", part, id)
	}
	d.Logf("%s matches its manifest: %d bytes, SHA-256 %s", part, n, want.SHA256)
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		done()
		return nil, nil, err
	}
	return f, done, nil
}

// restoreDatabase pipes the dump into mysql as root. A new database is
// made first and dropped again when the restore fails; an existing one is
// left as MySQL leaves it.
func restoreDatabase(ctx context.Context, d sysexec.Deps, env RestoreEnv, spool io.Reader, p *contract.BackupRestoreParams) error {
	if !p.Overwrite {
		if err := env.Create(ctx, p.Database); err != nil {
			return err
		}
	}
	zr, err := gzip.NewReader(spool)
	if err == nil {
		d.Logf("Restoring %s into %s", p.Part, p.Database)
		_, err = d.Run.Run(ctx, sysexec.Cmd{Path: mysqlPath, Args: []string{"--protocol=socket", "--user=root", string(p.Database)}, Stdin: zr})
	}
	if err == nil {
		d.Logf("Restored %s into %s", p.Part, p.Database)
		return nil
	}
	if p.Overwrite {
		return fmt.Errorf("restoring into %s failed; it may hold part of the backup: %w", p.Database, err)
	}
	uctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
	defer cancel()
	if derr := env.Drop(uctx, p.Database); derr != nil {
		return fmt.Errorf("restoring into %s failed: %w; dropping it again failed too, drop it with db.drop: %v", p.Database, err, derr)
	}
	d.Logf("Dropped the new database %s again", p.Database)
	return fmt.Errorf("restoring into %s failed, and the new database was dropped: %w", p.Database, err)
}

// restoreFiles extracts the archive into folder through the helper, as
// the site user. extract-tar refuses a link pointing out of the archive or
// through another link, and archive-tree stores links as they are (an
// absolute public/storage from storage:link, say), so such links are left
// out and named instead of failing the whole restore. A failed extract
// removes the half-made folder.
func restoreFiles(ctx context.Context, d sysexec.Deps, h RestoreHelper, a helper.Account, spool *os.File, folder string) ([]string, error) {
	links, err := tarLinks(spool)
	if err != nil {
		return nil, err
	}
	skip := map[string]bool{}
	var skipped []string
	for name, target := range links {
		if helper.LinkRefused(name, target, links) != nil {
			skip[name] = true
			skipped = append(skipped, name)
		}
	}
	slices.Sort(skipped)
	for _, name := range skipped {
		d.Logf("Leaving out the link %s -> %s: it points outside the restore folder or through another link", name, links[name])
	}
	if _, err := spool.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	if err := h.Mkdir(ctx, a, "restores", 0o700); err != nil {
		return nil, err
	}
	pr, pw := io.Pipe()
	go func() { pw.CloseWithError(filterTar(spool, pw, skip)) }()
	err = h.ExtractTar(ctx, a, folder, pr, restoreLimits)
	pr.CloseWithError(errUploadEnded)
	if err != nil {
		if rerr := h.RemoveTree(context.WithoutCancel(ctx), a, folder); rerr != nil && !errors.Is(rerr, fs.ErrNotExist) {
			d.Logf("Could not remove the half-restored ~/%s: %v", folder, rerr)
		}
		return nil, fmt.Errorf("extracting into ~/%s: %w", folder, err)
	}
	d.Logf("Restored the files into ~/%s", folder)
	if len(skipped) > contract.MaxSkippedLinks {
		skipped = skipped[:contract.MaxSkippedLinks]
	}
	return skipped, nil
}

// tarLinks are an archive's symlink members and their targets, by clean
// name as extract-tar keys them.
func tarLinks(r io.Reader) (map[string]string, error) {
	zr, err := gzip.NewReader(r)
	if err != nil {
		return nil, err
	}
	tr := tar.NewReader(zr)
	links := map[string]string{}
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return links, nil
		}
		if err != nil {
			return nil, err
		}
		if h.Typeflag == tar.TypeSymlink {
			links[path.Clean(h.Name)] = h.Linkname
		}
	}
}

// filterTar copies the gzip tarball r to w without the symlinks in skip.
func filterTar(r io.Reader, w io.Writer, skip map[string]bool) error {
	zr, err := gzip.NewReader(r)
	if err != nil {
		return err
	}
	tr := tar.NewReader(zr)
	zw := gzip.NewWriter(w)
	tw := tar.NewWriter(zw)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if h.Typeflag == tar.TypeSymlink && skip[path.Clean(h.Name)] {
			continue
		}
		if err := tw.WriteHeader(h); err != nil {
			return err
		}
		if _, err := io.Copy(tw, tr); err != nil {
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	return zw.Close()
}
