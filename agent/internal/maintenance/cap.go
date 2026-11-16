package maintenance

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"zimam/agent/contract"
	"zimam/agent/internal/helper"
	"zimam/agent/internal/sites"
	"zimam/agent/internal/sysexec"
)

// Homes is what the per-site cap needs inside sites' homes: the helper's
// list and remove-tree, as the site user (§5.6, §11.4). Root never lists
// or deletes there itself.
type Homes struct {
	Helper interface {
		List(ctx context.Context, a helper.Account, dir string) (helper.Listing, error)
		RemoveTree(ctx context.Context, a helper.Account, name string) error
	}
	Lookup func(username string) (helper.Account, error)
}

// logFile is one file the cap counts.
type logFile struct {
	where   string // shown and returned: the full path
	name    string // inside the home, for the helper; "" for root's
	size    int64
	mod     time.Time
	archive bool
}

// enforceCaps keeps each site's logs within the log policy (§11.3, §11.4).
// It lists the site's Nginx logs and archives, read by root from
// /var/log/zimam/sites/<name>/, and its own logs/, listed by the helper,
// with a laravel site's site/shared/storage/logs counted as the home's too
// (R02-D8).
// First it deletes every archive last changed more than log_retention_days
// ago: logrotate's maxage deletes only when the log itself rotates, so an
// idle site's archives would never age out. Then the cap, in two budgets,
// oldest archive first: the site user's archives go while root's files plus
// the home's are over site_log_cap_bytes; root's Nginx archives go only
// while root's files alone are. Sizes are allocated bytes, not apparent
// ones, and an archive's age is min(mtime, now): the site user sets both.
// Only regular files count, links never; only archives are deleted. It
// returns the paths it deleted.
func enforceCaps(ctx context.Context, d sysexec.Deps, homes Homes) ([]string, error) {
	all, err := sites.All(d.Paths)
	if err != nil {
		return nil, err
	}
	capBytes := int64(d.Config.SiteLogCapBytes)
	expired := d.Now().Add(-time.Duration(d.Config.LogRetentionDays) * 24 * time.Hour)
	var deleted []string
	var errs []error
	for _, st := range all {
		files, acct, err := siteLogFiles(ctx, d, homes, st)
		if err != nil {
			errs = append(errs, fmt.Errorf("site %s's logs: %w", st.Name, err))
			continue
		}
		var rootTotal, homeTotal int64
		var archives []logFile
		for _, f := range files {
			if f.name == "" {
				rootTotal += f.size
			} else {
				homeTotal += f.size
			}
			if f.archive {
				archives = append(archives, f)
			}
		}
		sortOldest(archives, d.Now())
		// remove deletes one archive and reports whether it went.
		remove := func(a logFile, why string) bool {
			var err error
			if a.name != "" {
				err = homes.Helper.RemoveTree(ctx, acct, a.name)
			} else {
				err = removeRegular(a.where)
			}
			if err != nil {
				errs = append(errs, fmt.Errorf("deleting %s: %w", a.where, err))
				return false
			}
			d.Logf("Deleted %s: %s", a.where, why)
			deleted = append(deleted, a.where)
			if a.name == "" {
				rootTotal -= a.size
			} else {
				homeTotal -= a.size
			}
			return true
		}
		var kept []logFile
		for _, a := range archives {
			if !a.mod.Before(expired) || !remove(a, fmt.Sprintf("older than the %d-day retention", d.Config.LogRetentionDays)) {
				kept = append(kept, a)
			}
		}
		// Two budgets. The site user's archives go while all the site's
		// logs are over the cap; root's Nginx archives go only while root's
		// own logs are, so what the site user plants in its home never
		// costs root a file.
		for _, a := range kept {
			if rootTotal+homeTotal <= capBytes {
				break
			}
			if a.name != "" {
				remove(a, fmt.Sprintf("site %s's logs were over the %d-byte cap", st.Name, capBytes))
			}
		}
		for _, a := range kept {
			if rootTotal <= capBytes {
				break
			}
			if a.name == "" {
				remove(a, fmt.Sprintf("site %s's Nginx logs were over the %d-byte cap", st.Name, capBytes))
			}
		}
	}
	return deleted, errors.Join(errs...)
}

// sortOldest orders archives oldest first by min(mtime, now), then path: a
// file dated in the future, which the site user can set, sorts as new as
// now, never newer.
func sortOldest(a []logFile, now time.Time) {
	at := func(f logFile) time.Time {
		if f.mod.After(now) {
			return now
		}
		return f.mod
	}
	sort.SliceStable(a, func(i, j int) bool {
		if ti, tj := at(a[i]), at(a[j]); !ti.Equal(tj) {
			return ti.Before(tj)
		}
		return a[i].where < a[j].where
	})
}

func isLogFile(name string) (log, archive bool) {
	archive = contract.ArchiveName(name).Validate() == nil
	return archive || strings.HasSuffix(name, ".log"), archive
}

// laravelLogDir is where a laravel site's own logs are, inside its home:
// the shared storage every release links to (R02-D8).
const laravelLogDir = "site/shared/storage/logs"

// siteLogFiles lists a site's log files: root's, then the home's through
// the helper (skipped while the site user doesn't exist): logs/, and for a
// laravel site its shared storage's logs. A home folder that isn't there
// has nothing to count.
func siteLogFiles(ctx context.Context, d sysexec.Deps, homes Homes, st sites.State) ([]logFile, helper.Account, error) {
	name := st.Name
	var files []logFile
	dir := d.Paths.SiteLogDir(name)
	entries, err := os.ReadDir(dir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, helper.Account{}, err
	}
	for _, de := range entries {
		ok, archive := isLogFile(de.Name())
		if !ok || !de.Type().IsRegular() {
			continue
		}
		fi, err := de.Info()
		if err != nil {
			continue
		}
		size := fi.Size()
		if st, ok := fi.Sys().(*syscall.Stat_t); ok {
			size = st.Blocks * 512
		}
		files = append(files, logFile{where: filepath.Join(dir, de.Name()), size: size, mod: fi.ModTime(), archive: archive})
	}
	if homes.Helper == nil || homes.Lookup == nil {
		return files, helper.Account{}, nil
	}
	acct, err := homes.Lookup("s_" + string(name))
	if err != nil {
		return files, helper.Account{}, nil // no site user: nothing in a home to count
	}
	dirs := []string{"logs"}
	if st.Type == contract.SiteLaravel {
		dirs = append(dirs, laravelLogDir)
	}
	for _, dir := range dirs {
		l, err := homes.Helper.List(ctx, acct, dir)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, acct, err
		}
		for _, fi := range l.Entries {
			ok, archive := isLogFile(fi.Name)
			if !ok || !fi.Mode.IsRegular() {
				continue
			}
			rel := path.Join(dir, fi.Name)
			files = append(files, logFile{where: path.Join(acct.Home, rel), name: rel, size: fi.BlocksBytes, mod: fi.ModTime, archive: archive})
		}
	}
	return files, acct, nil
}

// removeRegular deletes a root-owned file, unless it has become something
// other than a regular file since it was listed.
func removeRegular(p string) error {
	fi, err := os.Lstat(p)
	if err != nil {
		return err
	}
	if !fi.Mode().IsRegular() {
		return fmt.Errorf("%s isn't a regular file any more", p)
	}
	return os.Remove(p)
}
