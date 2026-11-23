package backup

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"strings"
	"time"

	"zimam/agent/contract"
	"zimam/agent/internal/bootstrap"
	"zimam/agent/internal/helper"
	"zimam/agent/internal/sysexec"
)

const mysqldumpPath = "/usr/bin/mysqldump"

// dumpArgs dump one database as MySQL's root over the socket, which needs no
// password (auth_socket), in one consistent transaction (R05-D5).
var dumpArgs = []string{"--protocol=socket", "--user=root", "--single-transaction", "--quick", "--routines", "--events",
	"--triggers", "--no-tablespaces"}

// dumpTrailer starts mysqldump's last line. A killed dump stops mid-INSERT
// and rclone stores a short stream as if it were whole, so a dump without
// it is incomplete whatever its exit status (Task 1).
const dumpTrailer = "-- Dump completed"

// cleanupTimeout bounds deleting an incomplete object, which runs even when
// the run was cancelled.
const cleanupTimeout = 2 * time.Minute

// Helper is what backups ask of the site-user helper (D5): archives of a
// home's paths, a link's target, and removing a file the agent made there.
type Helper interface {
	ArchiveTree(ctx context.Context, a helper.Account, w io.Writer, paths ...string) error
	Stat(ctx context.Context, a helper.Account, name string) (helper.FileInfo, error)
	RemoveTree(ctx context.Context, a helper.Account, name string) error
}

// Source is one part's producer: it writes the part's whole stream to w and
// returns nil only when the stream is complete.
type Source struct {
	Part  contract.BackupPart
	write func(ctx context.Context, w io.Writer) error
	gzip  bool // the agent compresses it (a dump); archives come compressed
	dump  bool // it must end with mysqldump's trailer
}

// DatabaseSource is a database part: mysqldump as root (R05-D5).
func DatabaseSource(d sysexec.Deps, db contract.DatabaseName) Source {
	return Source{Part: contract.BackupPart("db-" + db), gzip: true, dump: true,
		write: func(ctx context.Context, w io.Writer) error {
			_, err := d.Run.Run(ctx, sysexec.Cmd{Path: mysqldumpPath, Args: append(dumpArgs[:len(dumpArgs):len(dumpArgs)], string(db)), Stdout: w})
			return err
		}}
}

// ArchiveSource is a gzip tarball of paths in a's home, made by the helper
// as a (R05-D4).
func ArchiveSource(h Helper, a helper.Account, part contract.BackupPart, paths ...string) Source {
	return Source{Part: part, write: func(ctx context.Context, w io.Writer) error {
		return h.ArchiveTree(ctx, a, w, paths...)
	}}
}

// panelSnapshot is where zimam:snapshot-db writes the panel's database copy,
// relative to zimam's home.
const panelSnapshot = "panel/shared/zimam-backup.sqlite"

// PanelSource is the panel's database: zimam:snapshot-db makes a consistent
// copy (VACUUM INTO) as zimam, the helper archives it, and the copy is
// removed again (R05-D5). VACUUM INTO refuses an existing file, so a copy a
// killed run left is removed first (Task 1).
func PanelSource(d sysexec.Deps, h Helper, zimam helper.Account) Source {
	return Source{Part: contract.PanelPart, write: func(ctx context.Context, w io.Writer) error {
		if err := h.RemoveTree(ctx, zimam, panelSnapshot); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		defer h.RemoveTree(context.WithoutCancel(ctx), zimam, panelSnapshot)
		if _, err := bootstrap.Artisan(ctx, d, d.Paths.PanelCurrent()+"/artisan", "zimam:snapshot-db", d.Paths.PanelHome()+"/"+panelSnapshot); err != nil {
			return fmt.Errorf("snapshotting the panel's database: %w", err)
		}
		return h.ArchiveTree(ctx, zimam, w, panelSnapshot)
	}}
}

// SiteFiles are the paths a site's files part holds (R05-D4, OQ4): its
// shared folder when it has one, and the active release under its own name,
// found from site/current's target. Archiving through site/current would
// put the release one folder shallower and break its relative links on
// restore (Task 5), so the target must be exactly releases/<id>.
func SiteFiles(ctx context.Context, h Helper, a helper.Account) ([]string, error) {
	fi, err := h.Stat(ctx, a, "site/current")
	if err != nil {
		return nil, fmt.Errorf("finding the active release: %w", err)
	}
	id, ok := strings.CutPrefix(fi.Target, "releases/")
	if fi.Mode&fs.ModeSymlink == 0 || !ok || (id != "initial" && contract.ReleaseID(id).Validate() != nil) {
		return nil, fmt.Errorf("site/current of %s isn't a link to releases/<release>", a.Name)
	}
	var out []string
	switch shared, err := h.Stat(ctx, a, "site/shared"); {
	case err == nil && shared.Mode.IsDir():
		out = append(out, "site/shared")
	case err != nil && !errors.Is(err, fs.ErrNotExist):
		return nil, err
	}
	return append(out, "site/releases/"+id), nil
}

// ObjectName is a part's object in its backup's folder.
func ObjectName(p contract.BackupPart) string {
	if _, ok := p.Database(); ok {
		return string(p) + ".sql.gz"
	}
	return string(p) + ".tar.gz"
}

// errUploadEnded is what a producer gets when rclone stopped reading.
var errUploadEnded = errors.New("rclone stopped reading the stream before its end")

// Pipe streams src to the object dst ("<dest>:<plan>/<backup>/<object>")
// through rclone rcat, hashing and counting what goes up (R05-D5). The part
// counts only when the producer finished its stream, every stage took all
// of it and rclone exited 0; otherwise both ends are stopped, the object
// rclone may have stored is deleted, and the error says why. Nothing holds
// the stream: memory stays flat whatever its size.
func Pipe(ctx context.Context, d sysexec.Deps, src Source, dst string) (contract.BackupPartInfo, error) {
	info := contract.BackupPartInfo{Part: src.Part}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	pr, pw := io.Pipe()
	sum := sha256.New()
	var n counter
	produced := make(chan error, 1)
	go func() {
		err := produce(ctx, src, io.MultiWriter(pw, sum, &n))
		if err != nil {
			cancel() // stop rclone before it stores a short stream as whole
		}
		pw.CloseWithError(err)
		produced <- err
	}()
	_, upErr := rcloneRun(ctx, d, pr, "rcat", dst)
	pr.CloseWithError(errUploadEnded)
	if upErr != nil {
		cancel()
	}
	err := <-produced
	switch {
	case upErr != nil && (err == nil || errors.Is(err, errUploadEnded)):
		err = fmt.Errorf("%s: uploading: %w", src.Part, upErr)
	case err != nil:
		err = fmt.Errorf("%s: %w", src.Part, err)
	}
	if err != nil {
		deleteObject(ctx, d, dst)
		return info, err
	}
	info.Bytes, info.SHA256 = contract.DatabaseSize(n), hex.EncodeToString(sum.Sum(nil))
	d.Logf("Uploaded %s: %d bytes, SHA-256 %s", src.Part, n, info.SHA256)
	return info, nil
}

// produce writes src's stream to w, gzipped if it asks, and checks a dump's
// trailer.
func produce(ctx context.Context, src Source, w io.Writer) error {
	var gz *gzip.Writer
	if src.gzip {
		gz = gzip.NewWriter(w)
		w = gz
	}
	var tail tailWriter
	if src.dump {
		w = io.MultiWriter(w, &tail)
	}
	if err := src.write(ctx, w); err != nil {
		return err
	}
	if src.dump && !tail.lastLineHasPrefix(dumpTrailer) {
		return fmt.Errorf("the dump doesn't end with mysqldump's %q line: it stopped early", dumpTrailer)
	}
	if gz != nil {
		return gz.Close()
	}
	return nil
}

// deleteObject removes an object a failed part may have left, even after
// the run was cancelled.
func deleteObject(ctx context.Context, d sysexec.Deps, dst string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
	defer cancel()
	if _, err := rcloneRun(ctx, d, nil, "deletefile", dst); err != nil {
		d.Logf("Could not delete the incomplete %s (it may never have been stored): %v", dst, err)
		return
	}
	d.Logf("Deleted the incomplete %s", dst)
}

type counter int64

func (c *counter) Write(p []byte) (int, error) { *c += counter(len(p)); return len(p), nil }

// tailWriter keeps the last bytes of a stream, enough for its last line.
type tailWriter struct{ buf []byte }

const tailBytes = 256

func (t *tailWriter) Write(p []byte) (int, error) {
	if len(p) >= tailBytes {
		t.buf = append(t.buf[:0], p[len(p)-tailBytes:]...)
		return len(p), nil
	}
	if drop := len(t.buf) + len(p) - tailBytes; drop > 0 {
		t.buf = t.buf[:copy(t.buf, t.buf[drop:])]
	}
	t.buf = append(t.buf, p...)
	return len(p), nil
}

func (t *tailWriter) lastLineHasPrefix(prefix string) bool {
	b := bytes.TrimSuffix(t.buf, []byte("\n"))
	if i := bytes.LastIndexByte(b, '\n'); i >= 0 {
		b = b[i+1:]
	}
	return bytes.HasPrefix(b, []byte(prefix))
}
