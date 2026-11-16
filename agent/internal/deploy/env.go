package deploy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"strings"

	"zimam/agent/contract"
	"zimam/agent/internal/helper"
	"zimam/agent/internal/sites"
	"zimam/agent/internal/sysexec"
)

// envFile is a site's shared .env, which every deploy links into its
// release (R02-D10).
const envFile = sharedDir + "/.env"

// EnvReadHelper is what site.env.read needs of the site-user helper.
type EnvReadHelper interface {
	statter
	Read(ctx context.Context, a helper.Account, name string, offset, limit int64) (helper.ReadResult, error)
}

// EnvWriteHelper is what site.env.write needs of the site-user helper.
type EnvWriteHelper interface {
	statter
	Mkdir(ctx context.Context, a helper.Account, name string, mode fs.FileMode) error
	Write(ctx context.Context, a helper.Account, name string, mode fs.FileMode, content io.Reader) error
}

var (
	_ EnvReadHelper  = (*helper.Client)(nil)
	_ EnvWriteHelper = (*helper.Client)(nil)
)

// envConflict refuses the .env as it is on disk. Its message never quotes
// the file: the content is a secret.
func envConflict(name contract.SiteName, why string) error {
	return &contract.AgentError{Code: contract.ErrConflict, Message: fmt.Sprintf("Site %s's ~/%s %s.", name, envFile, why)}
}

// ReadEnv is site.env.read: the site's shared .env, read as the site user
// through the helper (R02-D10). Any site whose ~/site/shared/.env exists
// has one; a site without it is not_found. The file must be a regular
// file: a link is refused wherever it points, as a log's is. A file over
// 64 KiB, or one that isn't UTF-8 without NUL, is refused, since the panel
// couldn't save it back. The dispatcher audits the read, with the content
// redacted; nothing here logs it.
func ReadEnv(ctx context.Context, d sysexec.Deps, h EnvReadHelper, lookup sites.Lookup, name contract.SiteName) (contract.SiteEnvReadResult, error) {
	if _, err := loadSite(d, name); err != nil {
		return contract.SiteEnvReadResult{}, err
	}
	a, err := lookup("s_" + string(name))
	if err != nil {
		return contract.SiteEnvReadResult{}, err
	}
	fi, err := h.Stat(ctx, a, envFile)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return contract.SiteEnvReadResult{}, &contract.AgentError{Code: contract.ErrNotFound, Message: fmt.Sprintf("Site %s has no .env (~/%s).", name, envFile)}
	case err != nil:
		return contract.SiteEnvReadResult{}, err
	case !fi.Mode.IsRegular():
		return contract.SiteEnvReadResult{}, envConflict(name, "isn't a regular file")
	case fi.Size > contract.MaxTextFileBytes:
		return contract.SiteEnvReadResult{}, envConflict(name, "is over 64 KiB")
	}
	// One byte over the cap tells a file that grew since the stat.
	r, err := h.Read(ctx, a, envFile, 0, contract.MaxTextFileBytes+1)
	if err != nil {
		return contract.SiteEnvReadResult{}, err
	}
	if len(r.Data) > contract.MaxTextFileBytes || r.Size > contract.MaxTextFileBytes {
		return contract.SiteEnvReadResult{}, envConflict(name, "is over 64 KiB")
	}
	content := contract.EnvContent(r.Data)
	if content.Validate() != nil {
		return contract.SiteEnvReadResult{}, envConflict(name, "isn't UTF-8 text without NUL characters, so it can't be edited here")
	}
	return contract.SiteEnvReadResult{Content: content}, nil
}

// CheckEnvWrite is site.env.write's precheck: the site must exist
// (not_found).
func CheckEnvWrite(d sysexec.Deps, p contract.SiteEnvWriteParams) error {
	_, err := loadSite(d, p.Name)
	return err
}

// WriteEnv is site.env.write: it replaces the site's shared .env, as the
// site user through the helper's write, mode 0600 (R02-D10). The write is
// a temporary file renamed into place, so a planted .env link is replaced,
// never written through.
//
// Any site may have a .env: a site without ~/site/shared (a static or PHP
// site) gets it, 0750 as a laravel site's, and the next deploy links the
// .env into its release, as it does for every type. A ~/site/shared that
// isn't a directory is refused. The content goes to the helper on stdin
// only: never into argv, the job log or an error.
func WriteEnv(ctx context.Context, d sysexec.Deps, h EnvWriteHelper, lookup sites.Lookup, p contract.SiteEnvWriteParams) (contract.SiteNameResult, error) {
	if err := p.Validate(); err != nil {
		return contract.SiteNameResult{}, err
	}
	if _, err := loadSite(d, p.Name); err != nil {
		return contract.SiteNameResult{}, err
	}
	a, err := lookup("s_" + string(p.Name))
	if err != nil {
		return contract.SiteNameResult{}, err
	}
	fi, err := h.Stat(ctx, a, sharedDir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		d.Logf("Making ~/%s", sharedDir)
		if err := h.Mkdir(ctx, a, sharedDir, 0o750); err != nil {
			return contract.SiteNameResult{}, err
		}
	case err != nil:
		return contract.SiteNameResult{}, err
	case !fi.Mode.IsDir():
		return contract.SiteNameResult{}, &contract.AgentError{Code: contract.ErrConflict,
			Message: fmt.Sprintf("Site %s's ~/%s isn't a directory.", p.Name, sharedDir)}
	}
	d.Logf("Writing ~/%s, mode 0600", envFile)
	if err := h.Write(ctx, a, envFile, 0o600, strings.NewReader(string(p.Content))); err != nil {
		return contract.SiteNameResult{}, err
	}
	d.Logf("Site %s's .env is saved", p.Name)
	return contract.SiteNameResult{Name: p.Name}, nil
}
