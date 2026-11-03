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

// enforceCaps keeps each site's logs under site_log_cap_bytes (§11.4): it
// adds up the site's Nginx logs and archives, read by root from
// /var/log/zimam/sites/<name>/, and its own logs/, listed by the helper,
// and while the total is over the cap deletes the oldest archive. Only
// regular files count, links never; only archives are deleted. It returns
// the paths it deleted.
func enforceCaps(ctx context.Context, d sysexec.Deps, homes Homes) ([]string, error) {
	all, err := sites.All(d.Paths)
	if err != nil {
		return nil, err
	}
	capBytes := int64(d.Config.SiteLogCapBytes)
	var deleted []string
	var errs []error
	for _, st := range all {
		files, acct, err := siteLogFiles(ctx, d, homes, st.Name)
		if err != nil {
			errs = append(errs, fmt.Errorf("site %s's logs: %w", st.Name, err))
			continue
		}
		var total int64
		var archives []logFile
		for _, f := range files {
			total += f.size
			if f.archive {
				archives = append(archives, f)
			}
		}
		sort.SliceStable(archives, func(i, j int) bool {
			if !archives[i].mod.Equal(archives[j].mod) {
				return archives[i].mod.Before(archives[j].mod)
			}
			return archives[i].where < archives[j].where
		})
		for _, a := range archives {
			if total <= capBytes {
				break
			}
			if a.name != "" {
				err = homes.Helper.RemoveTree(ctx, acct, a.name)
			} else {
				err = removeRegular(a.where)
			}
			if err != nil {
				errs = append(errs, fmt.Errorf("deleting %s: %w", a.where, err))
				continue
			}
			d.Logf("Deleted %s: site %s's logs were over the %d-byte cap", a.where, st.Name, capBytes)
			deleted = append(deleted, a.where)
			total -= a.size
		}
	}
	return deleted, errors.Join(errs...)
}

func isLogFile(name string) (log, archive bool) {
	archive = contract.ArchiveName(name).Validate() == nil
	return archive || strings.HasSuffix(name, ".log"), archive
}

// siteLogFiles lists a site's log files: root's, then the home's through
// the helper (skipped while the site user doesn't exist).
func siteLogFiles(ctx context.Context, d sysexec.Deps, homes Homes, name contract.SiteName) ([]logFile, helper.Account, error) {
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
		files = append(files, logFile{where: filepath.Join(dir, de.Name()), size: fi.Size(), mod: fi.ModTime(), archive: archive})
	}
	if homes.Helper == nil || homes.Lookup == nil {
		return files, helper.Account{}, nil
	}
	acct, err := homes.Lookup("s_" + string(name))
	if err != nil {
		return files, helper.Account{}, nil // no site user: nothing in a home to count
	}
	l, err := homes.Helper.List(ctx, acct, "logs")
	if errors.Is(err, fs.ErrNotExist) {
		return files, acct, nil
	}
	if err != nil {
		return nil, acct, err
	}
	for _, fi := range l.Entries {
		ok, archive := isLogFile(fi.Name)
		if !ok || !fi.Mode.IsRegular() {
			continue
		}
		rel := path.Join("logs", fi.Name)
		files = append(files, logFile{where: path.Join(acct.Home, rel), name: rel, size: fi.Size, mod: fi.ModTime, archive: archive})
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
