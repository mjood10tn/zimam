// Package logs answers logs.list and logs.read (design §11): the fixed
// sources of §11.1, read by root from root-owned directories with
// O_NOFOLLOW, from a site's home through the helper, or from the journal.
// The panel only ever names a source, a site and an archive from the
// agent's own listing; it never sends a path.
package logs

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"
	"time"

	"zimam/agent/contract"
	"zimam/agent/internal/helper"
	"zimam/agent/internal/paths"
	"zimam/agent/internal/sites"
	"zimam/agent/internal/sysexec"
)

// Helper is what reading a site's PHP error log needs of the site-user
// helper (§5.6): root never opens a file inside a home.
type Helper interface {
	Stat(ctx context.Context, a helper.Account, name string) (helper.FileInfo, error)
	List(ctx context.Context, a helper.Account, dir string) (helper.Listing, error)
	Read(ctx context.Context, a helper.Account, name string, offset, limit int64) (helper.ReadResult, error)
	Tail(ctx context.Context, a helper.Account, name string, before int64, lines int) (helper.Lines, error)
	Search(ctx context.Context, a helper.Account, name string, from int64, q contract.LogSearch, limit int) (helper.SearchResult, error)
}

// Env is what the log commands run with.
type Env struct {
	D      sysexec.Deps
	Helper Helper
	Lookup func(username string) (helper.Account, error)
}

type kind int

const (
	rootFile   kind = iota // read by root, O_NOFOLLOW, from a root-owned directory
	helperFile             // inside a site's home, through the helper
	journal
)

// source is one row of §11.1, resolved for a site where it needs one.
type source struct {
	kind  kind
	path  string // rootFile: the current file; helperFile: relative to the home
	units []string
	k     bool // the kernel's messages (journalctl -k)
	acct  helper.Account
}

// resolve finds a source's origin. Site sources need a site with a state
// file; journal.php-<v> takes any allowlisted version.
func resolve(e Env, id contract.LogSource, name contract.SiteName) (source, error) {
	p := e.D.Paths
	if id.IsSite() {
		if _, err := sites.Load(p, name); errors.Is(err, fs.ErrNotExist) {
			return source{}, &contract.AgentError{Code: contract.ErrNotFound, Message: fmt.Sprintf("Site %s doesn't exist.", name)}
		} else if err != nil {
			return source{}, err
		}
	}
	switch id {
	case "site.nginx-access":
		return source{kind: rootFile, path: p.SiteAccessLog(name)}, nil
	case "site.nginx-error":
		return source{kind: rootFile, path: p.SiteErrorLog(name)}, nil
	case "site.php-error":
		a, err := e.Lookup("s_" + string(name))
		if err != nil {
			return source{}, fmt.Errorf("looking up s_%s: %w", name, err)
		}
		return source{kind: helperFile, path: "logs/php-error.log", acct: a}, nil
	case "file.nginx-error":
		return source{kind: rootFile, path: at(p, "var/log/nginx/error.log")}, nil
	case "file.certbot":
		return source{kind: rootFile, path: at(p, "var/log/letsencrypt/letsencrypt.log")}, nil
	case "file.apt-history":
		return source{kind: rootFile, path: at(p, "var/log/apt/history.log")}, nil
	case "file.unattended-upgrades":
		return source{kind: rootFile, path: at(p, "var/log/unattended-upgrades/unattended-upgrades.log")}, nil
	case "file.zimam-audit":
		return source{kind: rootFile, path: p.AuditLog()}, nil
	case "journal.system":
		return source{kind: journal}, nil
	case "journal.kernel":
		return source{kind: journal, k: true}, nil
	case "journal.ssh":
		return source{kind: journal, units: []string{"ssh.service", "ssh.socket"}}, nil
	case "journal.nginx":
		return source{kind: journal, units: []string{"nginx.service"}}, nil
	case "journal.zimam-agent":
		return source{kind: journal, units: []string{"zimam-agent.service", "zimam-job-*"}}, nil
	case "journal.zimam-panel":
		return source{kind: journal, units: []string{"zimam-panel-queue.service", "zimam-panel-scheduler.service"}}, nil
	}
	if v, ok := strings.CutPrefix(string(id), "journal.php-"); ok && contract.PHPVersion(v).Validate() == nil {
		return source{kind: journal, units: []string{"php" + v + "-fpm.service"}}, nil
	}
	return source{}, &contract.AgentError{Code: contract.ErrValidationFailed, Message: "Invalid parameters", Fields: contract.FieldErrors{"source": "is not a known log source"}}
}

// at is a fixed path under the Paths root, for the Ubuntu logs §11.1 names.
func at(p paths.Paths, rel string) string { return filepath.Join(p.Root, "/", rel) }

// isArchiveOf reports whether name is one of base's archives: the archive
// pattern (§5.4) with base's own name before the date.
func isArchiveOf(name, base string) bool {
	return contract.ArchiveName(name).Validate() == nil && strings.HasPrefix(name, base+"-")
}

func file(name string, size int64, mod time.Time) contract.LogFile {
	return contract.LogFile{Name: name, Size: size, Modified: mod.UTC()}
}
