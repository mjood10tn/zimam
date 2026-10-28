package helper

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"regexp"
	"slices"
	"syscall"
	"time"

	"zimam/agent/contract"
)

// Limits on what one operation reads, writes or returns (§5.6).
const (
	MaxWriteBytes    = 16 << 20 // write's content
	MaxReadBytes     = 1 << 20  // one read, and the text of one tail or search
	MaxListEntries   = 10_000
	MaxScanEntries   = 100_000
	MaxTailLines     = 1000
	MaxSearchMatches = 2000
	SearchTimeout    = 10 * time.Second

	maxLineBytes = 16 << 10 // a longer line is cut
	maxScanPaths = 100      // world-writable paths a scan names
)

// FileInfo describes a file without following a symlink.
type FileInfo struct {
	Name    string      `json:"name"`
	Size    int64       `json:"size"`
	Mode    fs.FileMode `json:"mode"`
	ModTime time.Time   `json:"mod_time"`
	Target  string      `json:"target,omitempty"` // a symlink's target, from Stat
}

// ReadResult is one read, and the file's size at the time.
type ReadResult struct {
	Data []byte `json:"data"`
	Size int64  `json:"size"`
}

// Listing is a directory's entries, sorted by name. Truncated means the
// directory held more than the limit, and only that many are listed.
type Listing struct {
	Entries   []FileInfo `json:"entries"`
	Truncated bool       `json:"truncated"`
}

// ScanResult counts the world-writable entries a scan found, and names the
// first maxScanPaths. Truncated means it stopped at the entry limit.
type ScanResult struct {
	Count     int      `json:"count"`
	Paths     []string `json:"paths"`
	Scanned   int      `json:"scanned"`
	Truncated bool     `json:"truncated"`
}

// Line is a line of a log at its byte offset, without its newline. A line
// longer than maxLineBytes is cut there and marked Cut.
type Line struct {
	Offset int64  `json:"offset"`
	Text   string `json:"text"`
	Cut    bool   `json:"cut,omitempty"`
}

// Lines is a tail: its lines, and the file's size at the time.
type Lines struct {
	Lines []Line `json:"lines"`
	Size  int64  `json:"size"`
}

// SearchResult is a search's matching lines. Next is the offset where it
// stopped, for the next search to start from. Stopped says why: "end" of
// the file, "matches" (the limit), "time" (SearchTimeout) or "size"
// (MaxReadBytes of matching text).
type SearchResult struct {
	Matches []Line `json:"matches"`
	Next    int64  `json:"next"`
	Stopped string `json:"stopped"`
}

// mkdir creates name and any missing parents, then gives name its mode
// exactly, whatever the umask. An existing directory gets the mode too.
func mkdir(root *os.Root, name string, mode fs.FileMode) error {
	if err := root.MkdirAll(name, mode); err != nil {
		return err
	}
	return root.Chmod(name, mode)
}

// writeFile replaces name with r's content, at most max bytes: it writes a
// new temporary file beside name with O_EXCL, fsyncs it, and renames it into
// place. A failure leaves name as it was. A symlink at name is replaced,
// never followed.
func writeFile(root *os.Root, name string, mode fs.FileMode, r io.Reader, max int64) error {
	tmp := name + ".zimam-tmp-" + rand.Text()
	f, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	err = fill(f, mode, r, max)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = root.Rename(tmp, name)
	}
	if err != nil {
		root.Remove(tmp)
		return err
	}
	return syncDir(root, path.Dir(name))
}

func fill(f *os.File, mode fs.FileMode, r io.Reader, max int64) error {
	n, err := io.Copy(f, io.LimitReader(r, max+1))
	if err != nil {
		return err
	}
	if n > max {
		return fmt.Errorf("the content is over %d bytes", max)
	}
	if err := f.Chmod(mode); err != nil {
		return err
	}
	return f.Sync()
}

// syncDir makes a rename inside dir durable.
func syncDir(root *os.Root, dir string) error {
	d, err := root.Open(dir)
	if err != nil {
		return err
	}
	err = d.Sync()
	if cerr := d.Close(); err == nil {
		err = cerr
	}
	return err
}

// openRegular opens name for reading and refuses anything but a regular
// file. O_NONBLOCK keeps a planted FIFO from blocking the open.
func openRegular(root *os.Root, name string) (f *os.File, size int64, err error) {
	f, err = root.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, 0, err
	}
	fi, err := f.Stat()
	if err == nil && !fi.Mode().IsRegular() {
		err = fmt.Errorf("%s is not a regular file", name)
	}
	if err != nil {
		f.Close()
		return nil, 0, err
	}
	return f, fi.Size(), nil
}

// readFile reads at most limit bytes, and never more than MaxReadBytes, from
// offset in the regular file name.
func readFile(root *os.Root, name string, offset, limit int64) (ReadResult, error) {
	if offset < 0 || limit < 1 {
		return ReadResult{}, fmt.Errorf("offset %d, limit %d: want an offset of 0 or more and a limit of 1 or more", offset, limit)
	}
	f, size, err := openRegular(root, name)
	if err != nil {
		return ReadResult{}, err
	}
	defer f.Close()
	buf := make([]byte, min(limit, MaxReadBytes, max(size-offset, 0)))
	n, err := f.ReadAt(buf, offset)
	if err != nil && err != io.EOF {
		return ReadResult{}, err
	}
	return ReadResult{Data: buf[:n], Size: size}, nil
}

// stat describes name without following it: a symlink is reported, with
// its target, wherever it points.
func stat(root *os.Root, name string) (FileInfo, error) {
	fi, err := root.Lstat(name)
	if err != nil {
		return FileInfo{}, err
	}
	info := fileInfo(fi)
	if fi.Mode()&fs.ModeSymlink != 0 {
		if info.Target, err = root.Readlink(name); err != nil {
			return FileInfo{}, err
		}
	}
	return info, nil
}

func fileInfo(fi fs.FileInfo) FileInfo {
	return FileInfo{Name: fi.Name(), Size: fi.Size(), Mode: fi.Mode(), ModTime: fi.ModTime()}
}

// list describes dir's entries, sorted by name, without following
// symlinks. Of a directory with more than limit entries, it lists limit.
func list(root *os.Root, dir string, limit int) (Listing, error) {
	names, err := readNames(root, dir, limit+1)
	if err != nil {
		return Listing{}, err
	}
	l := Listing{Entries: []FileInfo{}}
	if len(names) > limit {
		names, l.Truncated = names[:limit], true
	}
	slices.Sort(names)
	for _, n := range names {
		fi, err := root.Lstat(path.Join(dir, n))
		if errors.Is(err, fs.ErrNotExist) {
			continue // removed since it was listed
		}
		if err != nil {
			return Listing{}, err
		}
		l.Entries = append(l.Entries, fileInfo(fi))
	}
	return l, nil
}

// readNames returns at most n of dir's names, in directory order.
func readNames(root *os.Root, dir string, n int) ([]string, error) {
	d, err := root.Open(dir)
	if err != nil {
		return nil, err
	}
	defer d.Close()
	names, err := d.Readdirnames(n)
	if err == io.EOF { // an empty directory
		err = nil
	}
	return names, err
}

// releaseTargetRe is the only symlink target symlink-swap accepts.
var releaseTargetRe = regexp.MustCompile(`^releases/[A-Za-z0-9][A-Za-z0-9._+-]{0,127}$`)

// symlinkSwap points link at target: it creates a temporary symlink beside
// link and renames it over link, so link is never missing. target is
// relative to link's directory and must be releases/<name>.
func symlinkSwap(root *os.Root, link, target string) error {
	if !releaseTargetRe.MatchString(target) {
		return fmt.Errorf("symlink target %q is not releases/<name>", target)
	}
	tmp := link + ".zimam-tmp-" + rand.Text()
	if err := root.Symlink(target, tmp); err != nil {
		return err
	}
	if err := root.Rename(tmp, link); err != nil {
		root.Remove(tmp)
		return err
	}
	return syncDir(root, path.Dir(link))
}

// removeTree deletes name and everything under it. The home itself stays:
// for "." only its contents go (§9.8).
func removeTree(root *os.Root, name string) error {
	if path.Clean(name) == "." {
		return removeContents(root, ".")
	}
	return root.RemoveAll(name)
}

// removeContents deletes everything inside dir. os.Root resolves every path
// inside the home, and RemoveAll removes a symlink itself, never its target,
// so a planted link can't steer the delete outside (§9.8).
func removeContents(root *os.Root, dir string) error {
	f, err := root.Open(dir)
	if err != nil {
		return err
	}
	names, err := f.Readdirnames(-1)
	f.Close()
	if err != nil {
		return err
	}
	for _, n := range names {
		if err := root.RemoveAll(path.Join(dir, n)); err != nil {
			return err
		}
	}
	return nil
}

// scanWorldWritable walks dir, without following symlinks, for files and
// directories anyone may write to. It stops after limit entries, dir
// included, and says so. Entries it can't read are skipped.
func scanWorldWritable(root *os.Root, dir string, limit int) (ScanResult, error) {
	res := ScanResult{Paths: []string{}}
	check := func(p string) (isDir bool, err error) {
		fi, err := root.Lstat(p)
		if err != nil {
			return false, err
		}
		res.Scanned++
		if fi.Mode()&fs.ModeSymlink == 0 && fi.Mode().Perm()&0o002 != 0 {
			res.Count++
			if len(res.Paths) < maxScanPaths {
				res.Paths = append(res.Paths, p)
			}
		}
		return fi.IsDir(), nil
	}
	start := path.Clean(dir)
	isDir, err := check(start)
	if err != nil || !isDir {
		return res, err
	}
	// Depth first, with a stack rather than recursion, holding no open
	// directory while it descends.
	stack := []string{start}
	for len(stack) > 0 {
		d := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		names, err := readNames(root, d, limit-res.Scanned+1)
		if err != nil {
			continue
		}
		slices.Sort(names)
		for _, n := range names {
			if res.Scanned == limit {
				res.Truncated = true
				return res, nil
			}
			p := path.Join(d, n)
			if isDir, err := check(p); err == nil && isDir {
				stack = append(stack, p)
			}
		}
	}
	return res, nil
}

func cutLine(offset int64, b []byte) Line {
	if len(b) > maxLineBytes {
		return Line{Offset: offset, Text: string(b[:maxLineBytes]), Cut: true}
	}
	return Line{Offset: offset, Text: string(b)}
}

// tail returns the last n lines that end at or before the byte offset
// before, or at the end of the file when before is negative or past it. It
// reads at most MaxReadBytes back, so a page of long lines holds fewer than
// n; the first line's offset is where the next page back ends. A window
// inside one long line comes back as that piece of it, at the window's start.
func tail(root *os.Root, name string, before int64, n int) (Lines, error) {
	if n < 1 || n > MaxTailLines {
		return Lines{}, fmt.Errorf("%d lines: want 1 to %d", n, MaxTailLines)
	}
	f, size, err := openRegular(root, name)
	if err != nil {
		return Lines{}, err
	}
	defer f.Close()
	end := size
	if before >= 0 && before < size {
		end = before
	}
	start := max(end-MaxReadBytes, 0)
	buf := make([]byte, end-start)
	k, err := f.ReadAt(buf, start)
	if err != nil && err != io.EOF {
		return Lines{}, err
	}
	buf = buf[:k] // the file may have shrunk
	// A window that starts inside a line leaves that line out.
	if start > 0 {
		i := bytes.IndexByte(buf, '\n')
		if i < 0 {
			return Lines{Lines: []Line{cutLine(start, buf)}, Size: size}, nil
		}
		buf, start = buf[i+1:], start+int64(i+1)
	}
	lines := []Line{}
	for len(buf) > 0 {
		line, rest, _ := bytes.Cut(buf, []byte("\n"))
		lines = append(lines, cutLine(start, line))
		start += int64(len(buf) - len(rest))
		buf = rest
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return Lines{Lines: lines, Size: size}, nil
}

// search reads the regular file name forward from the byte offset from, line
// by line, for lines that contain q in any case (contract.LogSearch.Match).
// It stops at the end of the file, at limit matches, once SearchTimeout has
// passed on now, or once the matches hold MaxReadBytes of text.
func search(root *os.Root, name string, from int64, q contract.LogSearch, limit int, now func() time.Time) (SearchResult, error) {
	if from < 0 || limit < 1 || limit > MaxSearchMatches {
		return SearchResult{}, fmt.Errorf("from %d, %d matches: want an offset of 0 or more and 1 to %d matches", from, limit, MaxSearchMatches)
	}
	if err := q.Validate(); err != nil {
		return SearchResult{}, fmt.Errorf("search text: %w", err)
	}
	f, _, err := openRegular(root, name)
	if err != nil {
		return SearchResult{}, err
	}
	defer f.Close()
	if _, err := f.Seek(from, io.SeekStart); err != nil {
		return SearchResult{}, err
	}
	lines := lineReader{r: bufio.NewReaderSize(f, 64<<10), offset: from}
	res := SearchResult{Matches: []Line{}, Next: from}
	deadline := now().Add(SearchTimeout)
	text := 0
	for {
		if !now().Before(deadline) {
			res.Stopped = "time"
			return res, nil
		}
		line, ok, err := lines.next()
		if err != nil {
			return SearchResult{}, err
		}
		if !ok {
			res.Stopped = "end"
			return res, nil
		}
		res.Next = lines.offset
		if !q.Match(line.Text) {
			continue
		}
		res.Matches = append(res.Matches, line)
		text += len(line.Text)
		switch {
		case len(res.Matches) == limit:
			res.Stopped = "matches"
			return res, nil
		case text >= MaxReadBytes:
			res.Stopped = "size"
			return res, nil
		}
	}
}

// lineReader reads lines with their offsets. A line longer than
// maxLineBytes is cut there, and the rest of it skipped.
type lineReader struct {
	r      *bufio.Reader
	offset int64 // where the next line starts
}

// next returns the next line, or ok false at the end of the file. A last
// line without a newline counts.
func (lr *lineReader) next() (line Line, ok bool, err error) {
	line.Offset = lr.offset
	var text []byte
	for {
		chunk, err := lr.r.ReadSlice('\n')
		lr.offset += int64(len(chunk))
		if err == nil {
			chunk = chunk[:len(chunk)-1]
		}
		if room := maxLineBytes - len(text); len(chunk) > room {
			text, line.Cut = append(text, chunk[:room]...), true
		} else {
			text = append(text, chunk...)
		}
		switch {
		case err == nil:
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		case err == io.EOF && lr.offset > line.Offset:
		case err == io.EOF:
			return Line{}, false, nil
		default:
			return Line{}, false, err
		}
		line.Text = string(text)
		return line, true, nil
	}
}
