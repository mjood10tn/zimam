package logs

import (
	"bufio"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
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
)

const (
	window       = 1 << 20  // how far back a read from a plain file looks
	maxLineBytes = 16 << 10 // a longer line is cut and marked
	maxGzipBytes = 2 << 30  // decompressed bytes one archive read may go through
)

// List answers logs.list (§11.2): the current file's size and time, and its
// archives, newest first. A journal source has neither.
func List(ctx context.Context, e Env, p contract.LogsListParams) (contract.LogsListResult, error) {
	src, err := resolve(e, p.Source, p.Name)
	if err != nil {
		return contract.LogsListResult{}, err
	}
	res := contract.LogsListResult{Source: p.Source, Archives: []contract.LogFile{}}
	if src.kind == journal {
		return res, nil
	}
	cur, archives, err := listing(ctx, e, src)
	if err != nil {
		return contract.LogsListResult{}, err
	}
	res.Current, res.Archives = cur, archives
	return res, nil
}

// listing is a file source's current file (nil when missing) and archives.
func listing(ctx context.Context, e Env, src source) (*contract.LogFile, []contract.LogFile, error) {
	base := path.Base(filepath.ToSlash(src.path))
	var cur *contract.LogFile
	archives := []contract.LogFile{}
	if src.kind == rootFile {
		entries, err := os.ReadDir(filepath.Dir(src.path))
		if errors.Is(err, fs.ErrNotExist) {
			return nil, archives, nil
		}
		if err != nil {
			return nil, nil, err
		}
		for _, de := range entries {
			if !de.Type().IsRegular() || (de.Name() != base && !isArchiveOf(de.Name(), base)) {
				continue
			}
			fi, err := de.Info()
			if err != nil {
				continue
			}
			f := file(de.Name(), fi.Size(), fi.ModTime())
			if de.Name() == base {
				cur = &f
			} else {
				archives = append(archives, f)
			}
		}
	} else {
		l, err := e.Helper.List(ctx, src.acct, path.Dir(src.path))
		if errors.Is(err, fs.ErrNotExist) {
			return nil, archives, nil
		}
		if err != nil {
			return nil, nil, err
		}
		for _, fi := range l.Entries {
			if !fi.Mode.IsRegular() || (fi.Name != base && !isArchiveOf(fi.Name, base)) {
				continue
			}
			f := file(fi.Name, fi.Size, fi.ModTime)
			if fi.Name == base {
				cur = &f
			} else {
				archives = append(archives, f)
			}
		}
	}
	sort.Slice(archives, func(i, j int) bool { return archives[i].Name > archives[j].Name })
	return cur, archives, nil
}

// readFile answers logs.read for a file source. An archive must be in the
// agent's own listing of the source (§5.4); it is decompressed on the fly.
func readFile(ctx context.Context, e Env, src source, p contract.LogsReadParams) (contract.LogsReadResult, error) {
	name := path.Base(filepath.ToSlash(src.path))
	if p.Archive != "" {
		_, archives, err := listing(ctx, e, src)
		if err != nil {
			return contract.LogsReadResult{}, err
		}
		found := false
		for _, a := range archives {
			found = found || a.Name == string(p.Archive)
		}
		if !found {
			return contract.LogsReadResult{}, &contract.AgentError{Code: contract.ErrNotFound, Message: fmt.Sprintf("%s has no archive %s.", p.Source, p.Archive)}
		}
		name = string(p.Archive)
	}
	dir := path.Dir(filepath.ToSlash(src.path))
	if src.kind == helperFile && p.Archive == "" {
		return readThroughHelper(ctx, e, src, p)
	}
	open := func(offset int64) (io.ReadCloser, int64, error) {
		if src.kind == rootFile {
			return openRoot(filepath.Join(filepath.FromSlash(dir), name), offset)
		}
		return openHelper(ctx, e, src.acct, path.Join(dir, name), offset)
	}
	if p.Archive != "" {
		return readStream(ctx, func() (io.Reader, func() error, error) {
			rc, _, err := open(0)
			if err != nil {
				return nil, nil, err
			}
			zr, err := gzip.NewReader(rc)
			if err != nil {
				rc.Close()
				return nil, nil, fmt.Errorf("%s isn't gzip: %w", name, err)
			}
			return io.LimitReader(zr, maxGzipBytes), rc.Close, nil
		}, p)
	}
	return readPlain(ctx, open, p)
}

// openRoot opens a log root reads: the last element with O_NOFOLLOW, and
// O_NONBLOCK so a FIFO can't block; only a regular file is read.
func openRoot(name string, offset int64) (io.ReadCloser, int64, error) {
	f, err := os.OpenFile(name, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, 0, err
	}
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		f.Close()
		return nil, 0, fmt.Errorf("%s isn't a regular file", name)
	}
	if offset > 0 {
		if _, err := f.Seek(min(offset, fi.Size()), io.SeekStart); err != nil {
			f.Close()
			return nil, 0, err
		}
	}
	return f, fi.Size(), nil
}

// helperReader reads a file in the home through the helper's read, one
// MiB at a time.
type helperReader struct {
	ctx  context.Context
	e    Env
	a    helper.Account
	name string
	off  int64
	size int64
}

func (r *helperReader) Read(b []byte) (int, error) {
	res, err := r.e.Helper.Read(r.ctx, r.a, r.name, r.off, int64(min(len(b), helper.MaxReadBytes)))
	if err != nil {
		return 0, err
	}
	r.size = res.Size
	if len(res.Data) == 0 {
		return 0, io.EOF
	}
	n := copy(b, res.Data)
	r.off += int64(n)
	return n, nil
}

func (r *helperReader) Close() error { return nil }

func openHelper(ctx context.Context, e Env, a helper.Account, name string, offset int64) (io.ReadCloser, int64, error) {
	fi, err := e.Helper.Stat(ctx, a, name)
	if err != nil {
		return nil, 0, err
	}
	if !fi.Mode.IsRegular() {
		return nil, 0, fmt.Errorf("%s isn't a regular file", name)
	}
	return &helperReader{ctx: ctx, e: e, a: a, name: name, off: offset, size: fi.Size}, fi.Size, nil
}

// readPlain reads an uncompressed file: back from an offset (or the end)
// through a window of at most 1 MiB, forward from an offset, or a search
// forward from an offset (or the start).
func readPlain(ctx context.Context, open func(int64) (io.ReadCloser, int64, error), p contract.LogsReadParams) (contract.LogsReadResult, error) {
	n := p.Limit.Lines()
	var from int64
	if p.Offset != nil {
		from = int64(*p.Offset)
	}
	if p.Search != "" {
		return readStream(ctx, func() (io.Reader, func() error, error) {
			rc, _, err := open(from)
			if err != nil {
				return nil, nil, err
			}
			return &offsetReader{r: rc, base: from}, rc.Close, nil
		}, p)
	}
	if p.Direction == contract.LogForward {
		rc, size, err := open(from)
		if err != nil {
			return contract.LogsReadResult{}, err
		}
		defer rc.Close()
		lines, end := scanLines(io.LimitReader(rc, window), min(from, size), n, -1, true)
		return fileResult(lines, end), nil
	}
	rc, size, err := open(0)
	if err != nil {
		return contract.LogsReadResult{}, err
	}
	rc.Close()
	before := size
	if p.Offset != nil {
		before = min(from, size)
	}
	start := max(0, before-window)
	rc, _, err = open(start)
	if err != nil {
		return contract.LogsReadResult{}, err
	}
	defer rc.Close()
	lines, _ := scanLines(io.LimitReader(rc, before-start), start, -1, before, start == 0)
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return fileResult(lines, before), nil
}

// readStream reads a stream that can't seek (an archive, or a search) from
// its start: back keeps the last lines before the offset, forward takes the
// lines from it, a search the matching ones.
func readStream(ctx context.Context, open func() (io.Reader, func() error, error), p contract.LogsReadParams) (contract.LogsReadResult, error) {
	r, closer, err := open()
	if err != nil {
		return contract.LogsReadResult{}, err
	}
	defer closer()
	var from int64 = -1
	if p.Offset != nil {
		from = int64(*p.Offset)
	}
	base := int64(0)
	if or, ok := r.(*offsetReader); ok {
		base = or.base
	}
	if p.Search != "" {
		matches, next, stopped := search(ctx, r, base, p.Search)
		res := fileResult(matches, next)
		res.SearchStopped = stopped
		return res, nil
	}
	n := p.Limit.Lines()
	if p.Direction == contract.LogForward {
		var lines []contract.FileLogLine
		end := int64(0)
		forEachLine(r, base, func(l contract.FileLogLine, next int64) bool {
			end = next
			if l.Offset < from {
				return true
			}
			lines = append(lines, l)
			return len(lines) < n
		})
		if len(lines) == 0 {
			end = max(end, from)
		}
		return fileResult(lines, end), nil
	}
	var ring []contract.FileLogLine
	end := int64(0)
	forEachLine(r, base, func(l contract.FileLogLine, next int64) bool {
		if from >= 0 && next > from {
			return false
		}
		end = next
		ring = append(ring, l)
		if len(ring) > n {
			ring = ring[1:]
		}
		return true
	})
	return fileResult(ring, end), nil
}

// fileResult sets where to read next: back from the first line, null at
// the start of the file; forward from end.
func fileResult(lines []contract.FileLogLine, end int64) contract.LogsReadResult {
	res := contract.LogsReadResult{Lines: lines, Entries: []contract.JournalEntry{}}
	if res.Lines == nil {
		res.Lines = []contract.FileLogLine{}
	}
	if len(lines) > 0 && lines[0].Offset > 0 {
		back := lines[0].Offset
		res.BackOffset = &back
	} else if len(lines) == 0 && end > 0 {
		res.BackOffset = &end
	}
	res.ForwardOffset = &end
	return res
}

// offsetReader marks a reader that starts at base in the file.
type offsetReader struct {
	r    io.Reader
	base int64
}

func (o *offsetReader) Read(b []byte) (int, error) { return o.r.Read(b) }

// forEachLine calls fn with each complete line from r, which starts at
// offset base, and the offset after it, until fn answers false. A last line
// without its newline is left for a later read. Longer lines are cut.
func forEachLine(r io.Reader, base int64, fn func(l contract.FileLogLine, next int64) bool) {
	br := bufio.NewReaderSize(r, 64<<10)
	off := base
	for {
		raw, err := br.ReadSlice('\n')
		var long []byte
		for errors.Is(err, bufio.ErrBufferFull) {
			long = append(long, raw...)
			raw, err = br.ReadSlice('\n')
		}
		line := append(long, raw...)
		if err != nil {
			return // EOF or a line still being written
		}
		next := off + int64(len(line))
		text := strings.TrimSuffix(string(line), "\n")
		l := contract.FileLogLine{Offset: off, Text: text}
		if len(text) > maxLineBytes {
			l.Text, l.Cut = text[:maxLineBytes], true
		}
		if !fn(l, next) {
			return
		}
		off = next
	}
}

// scanLines reads the lines of r, which starts at base: at most n (all if
// n < 0), ending at or before stop (no stop if stop < 0). Unless atStart,
// the first, partial line of the window is dropped. It answers the lines
// and the offset after the last one.
func scanLines(r io.Reader, base int64, n int, stop int64, atStart bool) ([]contract.FileLogLine, int64) {
	var lines []contract.FileLogLine
	end := base
	first := !atStart
	forEachLine(r, base, func(l contract.FileLogLine, next int64) bool {
		if first {
			first = false
			end = next
			return true
		}
		if stop >= 0 && next > stop {
			return false
		}
		lines = append(lines, l)
		end = next
		return n < 0 || len(lines) < n
	})
	return lines, end
}

// readThroughHelper reads the current PHP error log inside the home: tail
// back, read forward, search forward, all as the site user (§5.6).
func readThroughHelper(ctx context.Context, e Env, src source, p contract.LogsReadParams) (contract.LogsReadResult, error) {
	fi, err := e.Helper.Stat(ctx, src.acct, src.path)
	if err != nil {
		return contract.LogsReadResult{}, err
	}
	if !fi.Mode.IsRegular() {
		return contract.LogsReadResult{}, fmt.Errorf("%s isn't a regular file", src.path)
	}
	var from int64
	if p.Offset != nil {
		from = int64(*p.Offset)
	}
	toLines := func(hl []helper.Line) []contract.FileLogLine {
		out := make([]contract.FileLogLine, 0, len(hl))
		for _, l := range hl {
			out = append(out, contract.FileLogLine{Offset: l.Offset, Text: l.Text, Cut: l.Cut})
		}
		return out
	}
	switch {
	case p.Search != "":
		r, err := e.Helper.Search(ctx, src.acct, src.path, from, p.Search, helper.MaxSearchMatches)
		if err != nil {
			return contract.LogsReadResult{}, err
		}
		res := fileResult(toLines(r.Matches), r.Next)
		res.SearchStopped = r.Stopped
		return res, nil
	case p.Direction == contract.LogForward:
		return readPlain(ctx, func(off int64) (io.ReadCloser, int64, error) {
			return openHelper(ctx, e, src.acct, src.path, off)
		}, p)
	}
	before := int64(-1)
	if p.Offset != nil {
		before = from
	}
	t, err := e.Helper.Tail(ctx, src.acct, src.path, before, p.Limit.Lines())
	if err != nil {
		return contract.LogsReadResult{}, err
	}
	end := t.Size
	if before >= 0 {
		end = min(before, t.Size)
	}
	return fileResult(toLines(t.Lines), end), nil
}

// searchClock and searchTimeout are package variables so tests can drive
// a search past its time limit.
var (
	searchClock   = time.Now
	searchTimeout = 10 * time.Second
)

// MaxSearchMatches is where a search stops (§11.2).
const MaxSearchMatches = 2000

// search scans r, which starts at base, for lines holding q as plain text
// in any case (contract.LogSearch.Match, never a regex), until 2,000
// matches, 10 seconds or the end, and says which.
func search(ctx context.Context, r io.Reader, base int64, q contract.LogSearch) ([]contract.FileLogLine, int64, string) {
	start := searchClock()
	var matches []contract.FileLogLine
	next, stopped := base, "end"
	forEachLine(r, base, func(l contract.FileLogLine, after int64) bool {
		if ctx.Err() != nil || searchClock().Sub(start) >= searchTimeout {
			stopped = "time"
			return false
		}
		next = after
		if q.Match(l.Text) {
			matches = append(matches, l)
			if len(matches) >= MaxSearchMatches {
				stopped = "matches"
				return false
			}
		}
		return true
	})
	return matches, next, stopped
}
