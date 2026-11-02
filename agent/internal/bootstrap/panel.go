package bootstrap

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"zimam/agent/internal/helper"
	"zimam/agent/internal/release"
	"zimam/agent/internal/sysexec"
)

const (
	runuser   = "/usr/sbin/runuser"
	zimamHome = "/home/zimam"

	maxManifestBytes = 1 << 20
	maxSigBytes      = 1 << 10
	maxTarballBytes  = 256 << 20 // gzipped; 0.0.0-dev's is 14.5 MB
)

// extractLimits bound the panel release extract-tar unpacks. 0.0.0-dev
// has 18,189 members and 67 MB of files; vendor/ grows.
var extractLimits = helper.ExtractLimits{MaxBytes: 1 << 30, MaxEntries: 200_000}

// panelDirs are made in zimam's home before the release (§5.9): shared/
// holds what outlives a release. The tarball has no storage/, so the
// shared one is made here, with what Laravel and the panel's pool write to.
var panelDirs = []struct {
	name string
	mode fs.FileMode
}{
	{"panel", 0o750},
	{"panel/releases", 0o750},
	{"panel/shared", 0o700},
	{"panel/shared/storage", 0o750},
	{"panel/shared/storage/app", 0o750},
	{"panel/shared/storage/app/public", 0o750},
	{"panel/shared/storage/framework", 0o750},
	{"panel/shared/storage/framework/cache", 0o750},
	{"panel/shared/storage/framework/cache/data", 0o750},
	{"panel/shared/storage/framework/sessions", 0o750},
	{"panel/shared/storage/framework/views", 0o750},
	{"panel/shared/storage/logs", 0o750},
	{"panel/shared/storage/tmp", 0o750},
}

// sharedLinks are linked into each release, read from inside it.
var sharedLinks = []struct{ link, target string }{
	{".env", "../../shared/.env"},
	{"storage", "../../shared/storage"},
	{"database/database.sqlite", "../../../shared/database.sqlite"},
}

// Step 6: the panel release (§4 step 6). The release is unpacked, linked
// to shared/ and migrated before current moves to it, so current pointing
// at it means the whole step is done.

func (r *run) releaseDone(ctx context.Context) (bool, error) {
	fi, err := r.Helper.Stat(ctx, r.Zimam, "panel/current")
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return fi.Mode&fs.ModeSymlink != 0 && fi.Target == "releases/"+r.o.Version, nil
}

func (r *run) releaseApply(ctx context.Context) error {
	m, err := r.release(ctx)
	if err != nil {
		return err
	}
	name := "zimam-panel-" + r.o.Version + ".tar.gz"
	tarball, err := r.download(ctx, m, name)
	if err != nil {
		return err
	}
	defer os.Remove(tarball)

	h, a := r.Helper, r.Zimam
	for _, dir := range panelDirs {
		if err := h.Mkdir(ctx, a, dir.name, dir.mode); err != nil {
			return err
		}
	}
	if err := r.writeOnce(ctx, "panel/shared/.env", r.envFile); err != nil {
		return err
	}
	// An empty file is an empty SQLite database.
	if err := r.writeOnce(ctx, "panel/shared/database.sqlite", func() ([]byte, error) { return nil, nil }); err != nil {
		return err
	}

	rel := "panel/releases/" + r.o.Version
	// extract-tar never replaces a file, so a release a failed run left
	// half unpacked goes first. current doesn't point at it: the step
	// would be done.
	if _, err := h.Stat(ctx, a, rel); err == nil {
		r.Deps.Logf("Removing the partly unpacked %s", rel)
		if err := h.RemoveTree(ctx, a, rel); err != nil {
			return err
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	f, err := os.Open(tarball)
	if err != nil {
		return err
	}
	defer f.Close()
	r.Deps.Logf("Unpacking %s as zimam", name)
	if err := h.ExtractTar(ctx, a, rel, f, extractLimits); err != nil {
		return err
	}
	for _, l := range sharedLinks {
		if err := h.SymlinkSwap(ctx, a, rel+"/"+l.link, l.target); err != nil {
			return err
		}
	}
	r.Deps.Logf("Migrating the panel's database")
	if _, err := artisan(ctx, r.deps(), r.Deps.Paths.PanelReleasesDir()+"/"+r.o.Version+"/artisan", "migrate", "--force"); err != nil {
		return err
	}
	return h.SymlinkSwap(ctx, a, "panel/current", "releases/"+r.o.Version)
}

// writeOnce writes name (0600) through the helper unless it exists: a
// re-run keeps the panel's .env and database.
func (r *run) writeOnce(ctx context.Context, name string, content func() ([]byte, error)) error {
	_, err := r.Helper.Stat(ctx, r.Zimam, name)
	if err == nil || !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	b, err := content()
	if err != nil {
		return err
	}
	return r.Helper.Write(ctx, r.Zimam, name, 0o600, bytes.NewReader(b))
}

// envFile is the panel's .env on a server (§4 step 6), with a new APP_KEY.
// It never names ZIMAM_AGENT, so the panel talks to the socket.
func (r *run) envFile() ([]byte, error) {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	lines := []string{
		"# Written once by zimam-agent bootstrap. APP_KEY is a secret.",
		"APP_NAME=Zimam",
		"APP_ENV=production",
		"APP_KEY=base64:" + base64.StdEncoding.EncodeToString(key),
		"APP_DEBUG=false",
		"APP_URL=https://" + string(r.cfg.PanelDomain),
		"APP_LOCALE=en",
		"APP_FALLBACK_LOCALE=en",
		"APP_MAINTENANCE_DRIVER=file",
		"BCRYPT_ROUNDS=12",
		"LOG_CHANNEL=single",
		"LOG_LEVEL=warning",
		"DB_CONNECTION=sqlite",
		"SESSION_DRIVER=database",
		"SESSION_LIFETIME=120",
		"SESSION_ENCRYPT=false",
		"SESSION_SECURE_COOKIE=true",
		"BROADCAST_CONNECTION=log",
		"FILESYSTEM_DISK=local",
		"QUEUE_CONNECTION=database",
		"CACHE_STORE=database",
		"MAIL_MAILER=log",
	}
	return []byte(strings.Join(lines, "\n") + "\n"), nil
}

// release downloads the manifest of the release this run installs and
// verifies it with the build's key, once per run.
func (r *run) release(ctx context.Context) (*release.Manifest, error) {
	if r.manifest != nil {
		return r.manifest, nil
	}
	if len(r.PublicKey) != ed25519.PublicKeySize {
		return nil, errors.New("this agent carries no release key, so it can't verify a release: bootstrap needs a release build")
	}
	if !strings.HasPrefix(r.BaseURL, "https://") && !strings.HasPrefix(r.BaseURL, "http://") {
		return nil, fmt.Errorf("this agent's release base URL %q isn't an http or https URL", r.BaseURL)
	}
	data, err := r.fetch(ctx, "manifest.json", maxManifestBytes)
	if err != nil {
		return nil, err
	}
	sig, err := r.fetch(ctx, "manifest.json.sig", maxSigBytes)
	if err != nil {
		return nil, err
	}
	m, err := release.Verify(r.PublicKey, data, sig, r.Deps.Now(), r.AgentVersion)
	if err != nil {
		return nil, err
	}
	// A validly signed manifest of another release must not stand in.
	if m.Version != r.o.Version {
		return nil, fmt.Errorf("the manifest served for release %s is release %s's", r.o.Version, m.Version)
	}
	r.manifest = m
	return m, nil
}

// url is a file of the release this run installs.
func (r *run) url(name string) string {
	return r.BaseURL + "/download/v" + r.o.Version + "/" + name
}

func (r *run) get(ctx context.Context, name string) (*http.Response, error) {
	u := r.url(name)
	r.Deps.Logf("Downloading %s", u)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := r.Deps.HTTPClient().Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("downloading %s: %s", u, resp.Status)
	}
	return resp, nil
}

// fetch reads a small file of the release.
func (r *run) fetch(ctx context.Context, name string, max int64) ([]byte, error) {
	resp, err := r.get(ctx, name)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, max+1))
	if err != nil {
		return nil, fmt.Errorf("downloading %s: %w", r.url(name), err)
	}
	if int64(len(b)) > max {
		return nil, fmt.Errorf("%s is larger than %d bytes", r.url(name), max)
	}
	return b, nil
}

// download saves an artifact in the downloads directory (root 0700) and
// checks it against the verified manifest. It returns the file's path.
func (r *run) download(ctx context.Context, m *release.Manifest, name string) (string, error) {
	if _, ok := m.Artifacts[name]; !ok {
		return "", fmt.Errorf("artifact %s is not in the manifest", name)
	}
	resp, err := r.get(ctx, name)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	dir := r.Deps.Paths.DownloadsDir()
	tmp, err := os.CreateTemp(dir, "."+name+".*")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name()) // fails harmlessly once renamed
	n, err := io.Copy(tmp, io.LimitReader(resp.Body, maxTarballBytes+1))
	if err == nil && n > maxTarballBytes {
		err = fmt.Errorf("%s is larger than %d bytes", r.url(name), maxTarballBytes)
	}
	if err == nil {
		_, err = tmp.Seek(0, io.SeekStart)
	}
	if err == nil {
		err = m.CheckArtifact(name, tmp)
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return "", err
	}
	final := filepath.Join(dir, name)
	return final, os.Rename(tmp.Name(), final)
}

// artisan runs a panel command as zimam (§4): runuser, the panel's PHP,
// and the runner's environment plus HOME. It runs in /, so root never
// enters zimam's home.
func artisan(ctx context.Context, d sysexec.Deps, script string, args ...string) (sysexec.Result, error) {
	argv := append([]string{"-u", "zimam", "--", "/usr/bin/php" + string(d.Config.PanelPHP), script}, args...)
	return d.Run.Run(ctx, sysexec.Cmd{Path: runuser, Args: argv, Env: []string{"HOME=" + zimamHome}, Dir: "/"})
}

var setupLinkLine = regexp.MustCompile(`^https://[^\s/]+/setup/[A-Za-z0-9_-]{43}$`)

// SetupLink runs zimam:setup-link as zimam (§4). It returns the command's
// output, the link and then the panel IP allowlist, and the link alone.
// Nothing here logs either: the link is a secret until it is used.
func SetupLink(ctx context.Context, d sysexec.Deps, clearAllowlist bool) (output []byte, link string, err error) {
	args := []string{"zimam:setup-link"}
	if clearAllowlist {
		args = append(args, "--clear-ip-allowlist")
	}
	res, err := artisan(ctx, d, d.Paths.PanelCurrent()+"/artisan", args...)
	if err != nil {
		return nil, "", err
	}
	first, _, _ := strings.Cut(string(res.Stdout), "\n")
	if !setupLinkLine.MatchString(first) {
		return nil, "", errors.New("zimam:setup-link didn't print a setup link on its first line")
	}
	return res.Stdout, first, nil
}
