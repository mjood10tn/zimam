package logs

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"zimam/agent/contract"
	"zimam/agent/internal/helper"
	"zimam/agent/internal/paths"
	"zimam/agent/internal/sites"
	"zimam/agent/internal/sysexec"
)

// fakeHelper serves a home from memory; a name in links is a symlink.
type fakeHelper struct {
	files map[string][]byte
	links map[string]bool
	ops   []string
}

func (h *fakeHelper) Stat(_ context.Context, _ helper.Account, name string) (helper.FileInfo, error) {
	h.ops = append(h.ops, "stat "+name)
	if h.links[name] {
		return helper.FileInfo{Name: filepath.Base(name), Mode: fs.ModeSymlink | 0o777, Target: "/etc/shadow"}, nil
	}
	b, ok := h.files[name]
	if !ok {
		return helper.FileInfo{}, fs.ErrNotExist
	}
	return helper.FileInfo{Name: filepath.Base(name), Mode: 0o640, Size: int64(len(b))}, nil
}

func (h *fakeHelper) List(_ context.Context, _ helper.Account, dir string) (helper.Listing, error) {
	var l helper.Listing
	for name, b := range h.files {
		if filepath.Dir(name) == dir {
			l.Entries = append(l.Entries, helper.FileInfo{Name: filepath.Base(name), Mode: 0o640, Size: int64(len(b))})
		}
	}
	return l, nil
}

func (h *fakeHelper) Read(_ context.Context, _ helper.Account, name string, off, limit int64) (helper.ReadResult, error) {
	h.ops = append(h.ops, "read "+name)
	b := h.files[name]
	if off >= int64(len(b)) {
		return helper.ReadResult{Size: int64(len(b))}, nil
	}
	return helper.ReadResult{Data: b[off:min(int64(len(b)), off+limit)], Size: int64(len(b))}, nil
}

func (h *fakeHelper) Tail(_ context.Context, _ helper.Account, name string, before int64, n int) (helper.Lines, error) {
	h.ops = append(h.ops, "tail "+name)
	return helper.Lines{Lines: []helper.Line{{Offset: 0, Text: "PHP Warning: one"}}, Size: int64(len(h.files[name]))}, nil
}

func (h *fakeHelper) Search(_ context.Context, _ helper.Account, name string, from int64, q contract.LogSearch, limit int) (helper.SearchResult, error) {
	h.ops = append(h.ops, "search "+name)
	return helper.SearchResult{Stopped: "end"}, nil
}

type env struct {
	t   *testing.T
	e   Env
	h   *fakeHelper
	run *sysexec.FakeRunner
}

func newEnv(t *testing.T) *env {
	t.Helper()
	p := paths.Paths{Root: t.TempDir()}
	if err := sites.Save(p, sites.State{Name: "shop", Domain: "shop.example.com", Type: contract.SiteStatic, TemplateVersion: 1, Security: sites.DefaultSecurity()}); err != nil {
		t.Fatal(err)
	}
	h := &fakeHelper{files: map[string][]byte{}, links: map[string]bool{}}
	run := sysexec.NewFakeRunner()
	return &env{t: t, h: h, run: run, e: Env{
		D:      sysexec.Deps{Paths: p, Run: run, Logf: t.Logf, Now: time.Now},
		Helper: h,
		Lookup: func(string) (helper.Account, error) {
			return helper.Account{Name: "s_shop", UID: 1000, GID: 1000, Home: "/home/s_shop"}, nil
		},
	}}
}

func (e *env) write(path string, content []byte) {
	e.t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		e.t.Fatal(err)
	}
	if err := os.WriteFile(path, content, 0o640); err != nil {
		e.t.Fatal(err)
	}
}

// numbered is "line 1\n" … "line n\n".
func numbered(n int) []byte {
	var b bytes.Buffer
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&b, "line %d\n", i)
	}
	return b.Bytes()
}

func texts(ls []contract.FileLogLine) []string {
	var out []string
	for _, l := range ls {
		out = append(out, l.Text)
	}
	return out
}

func read(t *testing.T, e Env, p contract.LogsReadParams) contract.LogsReadResult {
	t.Helper()
	if err := p.Validate(); err != nil {
		t.Fatalf("Validate(%+v): %v", p, err)
	}
	res, err := Read(context.Background(), e, p)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	return res
}

func off(n int64) *contract.LogOffset { o := contract.LogOffset(n); return &o }

func wantCode(t *testing.T, err error, code contract.ErrorCode) {
	t.Helper()
	var ae *contract.AgentError
	if !errors.As(err, &ae) || ae.Code != code {
		t.Errorf("err = %v, want %s", err, code)
	}
}

func TestUnknownSourceRejected(t *testing.T) {
	for _, s := range []contract.LogSource{"file./etc/shadow", "journal.php-7.4", "site.access", ""} {
		p := contract.LogsReadParams{Source: s, Direction: contract.LogBack}
		if p.Validate() == nil {
			t.Errorf("%q passed validation", s)
		}
	}
}

func TestSiteSourceNeedsExistingSite(t *testing.T) {
	e := newEnv(t)
	p := contract.LogsListParams{Source: "site.nginx-access"}
	if p.Validate() == nil {
		t.Error("a site source without a name passed")
	}
	_, err := List(context.Background(), e.e, contract.LogsListParams{Source: "site.nginx-access", Name: "blog"})
	wantCode(t, err, contract.ErrNotFound)
	if (&contract.LogsListParams{Source: "journal.nginx", Name: "shop"}).Validate() == nil {
		t.Error("a server source with a name passed")
	}
}

// Only an archive in the agent's own listing is read: a well-formed name
// that isn't there is not_found, and a path never gets past validation.
func TestArchiveMustBeListed(t *testing.T) {
	e := newEnv(t)
	dir := e.e.D.Paths.SiteLogDir("shop")
	e.write(filepath.Join(dir, "access.log"), numbered(3))
	e.write(filepath.Join(dir, "access.log-20261101-1761955200.gz"), gz(t, numbered(2)))
	e.write(filepath.Join(dir, "error.log-20261101-1761955200.gz"), gz(t, numbered(2)))
	e.write(filepath.Join(dir, "access.log.1"), numbered(1))
	l, err := List(context.Background(), e.e, contract.LogsListParams{Source: "site.nginx-access", Name: "shop"})
	if err != nil {
		t.Fatal(err)
	}
	if l.Current == nil || l.Current.Size != int64(len(numbered(3))) || len(l.Archives) != 1 || l.Archives[0].Name != "access.log-20261101-1761955200.gz" {
		t.Errorf("listing = %+v", l)
	}
	_, err = Read(context.Background(), e.e, contract.LogsReadParams{Source: "site.nginx-access", Name: "shop", Archive: "access.log-20261102-1.gz", Direction: contract.LogBack})
	wantCode(t, err, contract.ErrNotFound)
	_, err = Read(context.Background(), e.e, contract.LogsReadParams{Source: "site.nginx-access", Name: "shop", Archive: "error.log-20261101-1761955200.gz", Direction: contract.LogBack})
	wantCode(t, err, contract.ErrNotFound)
}

func TestArchiveTraversalRejected(t *testing.T) {
	for _, a := range []contract.ArchiveName{"../../../../etc/shadow", "/etc/shadow", "access.log-1-1.gz/../../x", "access.log-20261101-1.gz\n"} {
		p := contract.LogsReadParams{Source: "file.zimam-audit", Archive: a, Direction: contract.LogBack}
		err := p.Validate()
		var ae *contract.AgentError
		if !errors.As(err, &ae) || ae.Fields["archive"] == "" {
			t.Errorf("archive %q: Validate = %v", a, err)
		}
	}
}

func TestReadBackFromEnd(t *testing.T) {
	e := newEnv(t)
	e.write(e.e.D.Paths.AuditLog(), append(numbered(300), "partial"...))
	res := read(t, e.e, contract.LogsReadParams{Source: "file.zimam-audit", Direction: contract.LogBack})
	if len(res.Lines) != 200 || res.Lines[0].Text != "line 101" || res.Lines[199].Text != "line 300" {
		t.Fatalf("lines %d: %q … %q", len(res.Lines), res.Lines[0].Text, res.Lines[len(res.Lines)-1].Text)
	}
	if *res.BackOffset != res.Lines[0].Offset {
		t.Errorf("back offset %d, want the first line's %d", *res.BackOffset, res.Lines[0].Offset)
	}
	// Load older: back from that offset.
	older := read(t, e.e, contract.LogsReadParams{Source: "file.zimam-audit", Direction: contract.LogBack, Offset: off(*res.BackOffset)})
	if got := texts(older.Lines); len(got) != 100 || got[0] != "line 1" || got[99] != "line 100" || older.BackOffset != nil {
		t.Errorf("older = %d lines, back %v", len(got), older.BackOffset)
	}
}

func TestReadForwardFromOffset(t *testing.T) {
	e := newEnv(t)
	content := numbered(10)
	e.write(e.e.D.Paths.AuditLog(), content)
	from := int64(bytes.Index(content, []byte("line 4\n")))
	res := read(t, e.e, contract.LogsReadParams{Source: "file.zimam-audit", Direction: contract.LogForward, Offset: off(from), Limit: 3})
	if got := texts(res.Lines); !slices.Equal(got, []string{"line 4", "line 5", "line 6"}) {
		t.Errorf("lines %v", got)
	}
	want := int64(bytes.Index(content, []byte("line 7\n")))
	if *res.ForwardOffset != want {
		t.Errorf("forward offset %d, want %d", *res.ForwardOffset, want)
	}
	// Following: nothing new past the end, and the offset stays.
	end := read(t, e.e, contract.LogsReadParams{Source: "file.zimam-audit", Direction: contract.LogForward, Offset: off(int64(len(content)))})
	if len(end.Lines) != 0 || *end.ForwardOffset != int64(len(content)) {
		t.Errorf("at the end: %+v", end)
	}
}

func TestLimitBounds(t *testing.T) {
	for limit, ok := range map[contract.LogLimit]bool{0: true, 1: true, 1000: true, 1001: false, -1: false} {
		p := contract.LogsReadParams{Source: "journal.system", Direction: contract.LogBack, Limit: limit}
		if (p.Validate() == nil) != ok {
			t.Errorf("limit %d: Validate = %v", limit, p.Validate())
		}
	}
	if contract.LogLimit(0).Lines() != 200 {
		t.Error("the default isn't 200")
	}
}

func gz(t *testing.T, b []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	zw.Write(b)
	zw.Close()
	return buf.Bytes()
}

func TestGzipArchive(t *testing.T) {
	e := newEnv(t)
	dir := filepath.Dir(e.e.D.Paths.AuditLog())
	e.write(e.e.D.Paths.AuditLog(), nil)
	e.write(filepath.Join(dir, "audit.log-20261101-1761955200.gz"), gz(t, numbered(250)))
	p := contract.LogsReadParams{Source: "file.zimam-audit", Archive: "audit.log-20261101-1761955200.gz", Direction: contract.LogBack}
	res := read(t, e.e, p)
	if len(res.Lines) != 200 || res.Lines[0].Text != "line 51" || res.Lines[199].Text != "line 250" {
		t.Fatalf("archive back: %d lines from %q", len(res.Lines), res.Lines[0].Text)
	}
	p.Direction, p.Offset, p.Limit = contract.LogForward, off(0), 2
	if got := texts(read(t, e.e, p).Lines); !slices.Equal(got, []string{"line 1", "line 2"}) {
		t.Errorf("archive forward: %v", got)
	}
	// A site's PHP log archives sit in its home and are read through the helper.
	e.h.files["logs/php-error.log"] = []byte("x\n")
	e.h.files["logs/php-error.log-20261101-1761955200.gz"] = gz(t, []byte("PHP Fatal error: boom\n"))
	res = read(t, e.e, contract.LogsReadParams{Source: "site.php-error", Name: "shop", Archive: "php-error.log-20261101-1761955200.gz", Direction: contract.LogBack})
	if got := texts(res.Lines); !slices.Equal(got, []string{"PHP Fatal error: boom"}) {
		t.Errorf("helper archive: %v", got)
	}
}

const journalOut = `{"__CURSOR":"s=a;i=1","__REALTIME_TIMESTAMP":"1762160400000000","PRIORITY":"6","_SYSTEMD_UNIT":"nginx.service","MESSAGE":"started"}
{"__CURSOR":"s=a;i=2","__REALTIME_TIMESTAMP":"1762160401000000","PRIORITY":"3","_SYSTEMD_UNIT":"nginx.service","MESSAGE":[104,105,255]}
`

func journalArgs(extra ...string) []string {
	return append([]string{"--output=json", "--no-pager", "-u", "nginx.service"}, extra...)
}

func TestJournalCursorPaging(t *testing.T) {
	e := newEnv(t)
	// Back from the end: newest first from journalctl, oldest first in the answer.
	e.run.Add(sysexec.Script{Path: journalctl, Args: journalArgs("--reverse", "-n", "200"), Result: sysexec.Result{Stdout: []byte(reverseLines(journalOut))}})
	res := read(t, e.e, contract.LogsReadParams{Source: "journal.nginx", Direction: contract.LogBack})
	if len(res.Entries) != 2 || res.Entries[0].Cursor != "s=a;i=1" || res.Entries[1].Message != "hi�" || res.Entries[1].Priority != 3 {
		t.Fatalf("entries %+v", res.Entries)
	}
	if *res.BackCursor != "s=a;i=1" || *res.ForwardCursor != "s=a;i=2" || !res.Entries[0].Time.Equal(time.Unix(1762160400, 0)) {
		t.Errorf("cursors %v %v, time %v", *res.BackCursor, *res.ForwardCursor, res.Entries[0].Time)
	}
	// Older: from the back cursor, whose own entry is dropped.
	e.run.Add(sysexec.Script{Path: journalctl, Args: journalArgs("--cursor=s=a;i=2", "--reverse", "-n", "11"), Result: sysexec.Result{Stdout: []byte(reverseLines(journalOut))}})
	older := read(t, e.e, contract.LogsReadParams{Source: "journal.nginx", Direction: contract.LogBack, Cursor: "s=a;i=2", Limit: 10})
	if len(older.Entries) != 1 || older.Entries[0].Cursor != "s=a;i=1" {
		t.Errorf("older %+v", older.Entries)
	}
	// Following: after the forward cursor.
	e.run.Add(sysexec.Script{Path: journalctl, Args: journalArgs("--after-cursor=s=a;i=2", "-n", "+200")})
	follow := read(t, e.e, contract.LogsReadParams{Source: "journal.nginx", Direction: contract.LogForward, Cursor: "s=a;i=2"})
	if len(follow.Entries) != 0 || *follow.ForwardCursor != "s=a;i=2" {
		t.Errorf("follow %+v", follow)
	}
	// Cursors reach journalctl only as validated arguments.
	if (&contract.LogsReadParams{Source: "journal.nginx", Direction: contract.LogBack, Cursor: "s=a; rm -rf /"}).Validate() == nil {
		t.Error("a cursor with a space passed")
	}
}

func reverseLines(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	slices.Reverse(lines)
	return strings.Join(lines, "\n") + "\n"
}

func TestPriorityFilter(t *testing.T) {
	e := newEnv(t)
	pr := contract.LogPriority(3)
	e.run.Add(sysexec.Script{Path: journalctl, Args: journalArgs("-p", "3", "--reverse", "-n", "200")})
	read(t, e.e, contract.LogsReadParams{Source: "journal.nginx", Direction: contract.LogBack, Priority: &pr})
	e.run.Add(sysexec.Script{Path: journalctl, Args: []string{"--output=json", "--no-pager", "-k", "--reverse", "-n", "200"}})
	read(t, e.e, contract.LogsReadParams{Source: "journal.kernel", Direction: contract.LogBack})
	if p := (contract.LogsReadParams{Source: "file.zimam-audit", Direction: contract.LogBack, Priority: &pr}); p.Validate() == nil {
		t.Error("a priority on a file source passed")
	}
	bad := contract.LogPriority(8)
	if p := (contract.LogsReadParams{Source: "journal.nginx", Direction: contract.LogBack, Priority: &bad}); p.Validate() == nil {
		t.Error("priority 8 passed")
	}
}

func searchAudit(t *testing.T, content []byte, q contract.LogSearch) contract.LogsReadResult {
	t.Helper()
	e := newEnv(t)
	e.write(e.e.D.Paths.AuditLog(), content)
	return read(t, e.e, contract.LogsReadParams{Source: "file.zimam-audit", Direction: contract.LogBack, Search: q})
}

// The search is plain text: ".*" finds only a literal ".*".
func TestSearchIsPlainText(t *testing.T) {
	res := searchAudit(t, []byte("anything\nliteral .* here\n[a-z]+\n"), ".*")
	if got := texts(res.Lines); !slices.Equal(got, []string{"literal .* here"}) || res.SearchStopped != "end" {
		t.Errorf("matches %v, stopped %q", got, res.SearchStopped)
	}
}

func TestSearchCaseInsensitive(t *testing.T) {
	res := searchAudit(t, []byte("Error one\nerror two\nERROR three\nwarning\n"), "eRRor")
	if got := texts(res.Lines); len(got) != 3 {
		t.Errorf("matches %v", got)
	}
}

func TestSearchStopsAt2000(t *testing.T) {
	var b bytes.Buffer
	for i := range 2500 {
		fmt.Fprintf(&b, "match %d\n", i)
	}
	res := searchAudit(t, b.Bytes(), "match")
	if len(res.Lines) != 2000 || res.SearchStopped != "matches" || res.Lines[1999].Text != "match 1999" {
		t.Errorf("%d matches, stopped %q", len(res.Lines), res.SearchStopped)
	}
	// The next search goes on from where this one stopped.
	if *res.ForwardOffset != int64(bytes.Index(b.Bytes(), []byte("match 2000\n"))) {
		t.Errorf("forward offset %d", *res.ForwardOffset)
	}
}

func TestSearchStopsAt10Seconds(t *testing.T) {
	now := time.Date(2026, 11, 3, 9, 0, 0, 0, time.UTC)
	old := searchClock
	searchClock = func() time.Time { now = now.Add(time.Second); return now }
	t.Cleanup(func() { searchClock = old })
	res := searchAudit(t, numbered(100), "line")
	if res.SearchStopped != "time" || len(res.Lines) >= 100 {
		t.Errorf("%d matches, stopped %q", len(res.Lines), res.SearchStopped)
	}
}

// A site user's symlink logs/php-error.log -> /etc/shadow gets an error and
// no content: the agent stats the file through the helper and refuses a
// symlink before anything reads it.
func TestPhpErrorThroughHelper(t *testing.T) {
	e := newEnv(t)
	e.h.files["logs/php-error.log"] = []byte("PHP Warning: one\n")
	res := read(t, e.e, contract.LogsReadParams{Source: "site.php-error", Name: "shop", Direction: contract.LogBack})
	if got := texts(res.Lines); !slices.Equal(got, []string{"PHP Warning: one"}) || !slices.Contains(e.h.ops, "tail logs/php-error.log") {
		t.Errorf("lines %v, ops %v", got, e.h.ops)
	}

	e = newEnv(t)
	e.h.links["logs/php-error.log"] = true
	for _, dir := range []contract.LogDirection{contract.LogBack, contract.LogForward} {
		p := contract.LogsReadParams{Source: "site.php-error", Name: "shop", Direction: dir, Offset: off(0)}
		res, err := Read(context.Background(), e.e, p)
		if err == nil || len(res.Lines) != 0 {
			t.Errorf("%s through a symlink: %+v, %v", dir, res, err)
		}
	}
	for _, op := range e.h.ops {
		if !strings.HasPrefix(op, "stat ") {
			t.Errorf("read through a symlink: %v", e.h.ops)
		}
	}
	// Root's own files are opened O_NOFOLLOW: a symlinked access.log is refused.
	dir := e.e.D.Paths.SiteLogDir("shop")
	os.MkdirAll(dir, 0o755)
	e.write(filepath.Join(t.TempDir(), "secret"), []byte("secret\n"))
	os.Symlink("/etc/passwd", filepath.Join(dir, "access.log"))
	if res, err := Read(context.Background(), e.e, contract.LogsReadParams{Source: "site.nginx-access", Name: "shop", Direction: contract.LogBack}); err == nil || len(res.Lines) != 0 {
		t.Errorf("a symlinked access.log was read: %+v", res)
	}
}
