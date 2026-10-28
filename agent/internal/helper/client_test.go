package helper

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"os/user"
	"slices"
	"strings"
	"testing"
)

// TestMain lets the client tests run this test binary as the helper: started
// as "as-user ...", it serves one operation, as zimam-agent does.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "as-user" {
		os.Exit(helperProcess(os.Args[2:]))
	}
	os.Exit(m.Run())
}

// helperProcess is the helper inside this test binary: Main, plus three
// operations that misbehave, for the client's output checks.
func helperProcess(args []string) int {
	if len(args) == 3 { // --home <home> <op>
		switch args[2] {
		case "test-flood":
			chunk := bytes.Repeat([]byte("x"), 1<<20)
			for n := 0; n <= maxStdout; n += len(chunk) {
				os.Stdout.Write(chunk)
			}
			return 0
		case "test-two-lines":
			fmt.Println("{}")
			fmt.Println("{}")
			return 0
		case "test-silent":
			return 0
		}
	}
	ids, err := ProcessIDs()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return Main(ids, args, os.Stdin, os.Stdout, os.Stderr)
}

// testClient returns a client that runs this test binary as the helper, as
// the current user. Without root the credential switch is left out; the
// rest (the process, its arguments, exit codes and output) is the real path.
func testClient(t *testing.T) (*Client, Account) {
	t.Helper()
	if os.Geteuid() == 0 {
		t.Skip("as-user refuses to run as root; helper_root_test.go covers the credential switch")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	c := &Client{AgentBinary: exe, prepare: func(cmd *exec.Cmd) {
		if cmd.SysProcAttr != nil {
			cmd.SysProcAttr.Credential = nil
		}
	}}
	a := Account{Name: "s_test", UID: uint32(os.Getuid()), GID: uint32(os.Getgid()), Home: t.TempDir()}
	return c, a
}

func TestCommandSetsCredentialAndEnv(t *testing.T) {
	c := &Client{AgentBinary: "/usr/local/sbin/zimam-agent"}
	a := Account{Name: "s_shop", UID: 1001, GID: 1002, Groups: []uint32{1002, 1003}, Home: "/home/s_shop"}
	cmd := c.command(context.Background(), a, "read", "logs/php-error.log", "0", "100")
	if cmd.Path != c.AgentBinary {
		t.Errorf("Path = %q, want %q", cmd.Path, c.AgentBinary)
	}
	wantArgs := []string{c.AgentBinary, "as-user", "--home", "/home/s_shop", "read", "logs/php-error.log", "0", "100"}
	if !slices.Equal(cmd.Args, wantArgs) {
		t.Errorf("Args = %q, want %q", cmd.Args, wantArgs)
	}
	if cmd.SysProcAttr == nil || cmd.SysProcAttr.Credential == nil {
		t.Fatal("no credential: the helper would run as root")
	}
	if cr := cmd.SysProcAttr.Credential; cr.Uid != 1001 || cr.Gid != 1002 || !slices.Equal(cr.Groups, []uint32{1002, 1003}) || cr.NoSetGroups {
		t.Errorf("Credential = %+v, want uid 1001, gid 1002, groups [1002 1003]", cr)
	}
	if !cmd.SysProcAttr.Setsid {
		t.Error("Setsid is off")
	}
	if want := []string{"PATH=/usr/bin:/bin", "HOME=/home/s_shop"}; !slices.Equal(cmd.Env, want) {
		t.Errorf("Env = %q, want %q", cmd.Env, want)
	}
	if cmd.Dir != "/" {
		t.Errorf("Dir = %q, want /", cmd.Dir)
	}
}

func fakeLookup(u *user.User, err error) func(string) (*user.User, error) {
	return func(string) (*user.User, error) { return u, err }
}

func fakeGroups(gids ...string) func(*user.User) ([]string, error) {
	return func(*user.User) ([]string, error) { return gids, nil }
}

func TestLookupAccountRefusesRoot(t *testing.T) {
	if _, err := LookupAccount("root"); err == nil {
		t.Error("LookupAccount(root) succeeded")
	}
	// An account that would carry root's ids is refused, whatever its name.
	for _, c := range []struct {
		u      *user.User
		groups []string
	}{
		{&user.User{Username: "zimam", Uid: "0", Gid: "998", HomeDir: "/home/zimam"}, []string{"998"}},
		{&user.User{Username: "zimam", Uid: "998", Gid: "0", HomeDir: "/home/zimam"}, []string{"0"}},
		{&user.User{Username: "s_shop", Uid: "1001", Gid: "1001", HomeDir: "/home/s_shop"}, []string{"1001", "0"}},
	} {
		if a, err := lookupAccount(c.u.Username, fakeLookup(c.u, nil), fakeGroups(c.groups...)); err == nil {
			t.Errorf("uid %s, gid %s, groups %q: got %+v, want an error", c.u.Uid, c.u.Gid, c.groups, a)
		}
	}
}

func TestLookupAccountRefusesOtherUsers(t *testing.T) {
	looked := false
	lookup := func(string) (*user.User, error) {
		looked = true
		return &user.User{Uid: "1001", Gid: "1001", HomeDir: "/home/x"}, nil
	}
	for _, name := range []string{
		"", "nobody", "www-data", "zimam-agent", "zimamx", "Zimam", "s_", "s_Shop", "S_shop", "s_shop.example",
		"s_../x", "s_shop/x", "s_-shop", "s_shop\n", "s_" + strings.Repeat("a", 25),
	} {
		if a, err := lookupAccount(name, lookup, fakeGroups("1001")); err == nil {
			t.Errorf("lookupAccount(%q) = %+v, want an error", name, a)
		}
	}
	if looked {
		t.Error("a refused name was looked up")
	}
}

func TestLookupAccountAcceptsSiteAndPanel(t *testing.T) {
	for _, u := range []*user.User{
		{Username: "zimam", Uid: "998", Gid: "997", HomeDir: "/home/zimam"},
		{Username: "s_shop", Uid: "1001", Gid: "1001", HomeDir: "/home/s_shop"},
	} {
		a, err := lookupAccount(u.Username, fakeLookup(u, nil), fakeGroups(u.Gid))
		gid := a.GID
		if err != nil || a.Name != u.Username || fmt.Sprint(a.UID) != u.Uid || fmt.Sprint(gid) != u.Gid ||
			!slices.Equal(a.Groups, []uint32{gid}) || a.Home != u.HomeDir {
			t.Errorf("lookupAccount(%s) = %+v, %v", u.Username, a, err)
		}
	}
	// The home must be /home/<name>.
	u := &user.User{Username: "s_shop", Uid: "1001", Gid: "1001", HomeDir: "/"}
	if _, err := lookupAccount("s_shop", fakeLookup(u, nil), fakeGroups("1001")); err == nil {
		t.Error("home / accepted")
	}
	// An account that doesn't exist is an error.
	if _, err := lookupAccount("s_shop", fakeLookup(nil, user.UnknownUserError("s_shop")), fakeGroups()); err == nil {
		t.Error("a missing account accepted")
	}
}

func TestClientRoundTrip(t *testing.T) {
	c, a := testClient(t)
	ctx := context.Background()
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(c.Mkdir(ctx, a, "site/releases/one", 0o750))
	must(c.Write(ctx, a, "site/releases/one/app.log", 0o640, strings.NewReader("first line\nan ERROR here\n")))
	must(c.SymlinkSwap(ctx, a, "site/current", "releases/one"))

	fi, err := c.Stat(ctx, a, "site/releases/one/app.log")
	if err != nil || fi.Name != "app.log" || fi.Size != 25 || fi.Mode != 0o640 {
		t.Errorf("Stat(app.log) = %+v, %v; want 25 bytes, 0640", fi, err)
	}
	cur, err := c.Stat(ctx, a, "site/current")
	if err != nil || cur.Mode&fs.ModeSymlink == 0 || cur.Target != "releases/one" {
		t.Errorf("Stat(current) = %+v, %v; want a link to releases/one", cur, err)
	}
	r, err := c.Read(ctx, a, "site/current/app.log", 11, 100)
	if err != nil || string(r.Data) != "an ERROR here\n" || r.Size != 25 {
		t.Errorf("Read = %q, size %d, %v", r.Data, r.Size, err)
	}
	l, err := c.List(ctx, a, "site")
	if err != nil || len(l.Entries) != 2 || l.Entries[0].Name != "current" || l.Entries[1].Name != "releases" || l.Truncated {
		t.Errorf("List(site) = %+v, %v", l, err)
	}
	tl, err := c.Tail(ctx, a, "site/current/app.log", -1, 1)
	if err != nil || tl.Size != 25 || !slices.Equal(tl.Lines, []Line{{Offset: 11, Text: "an ERROR here"}}) {
		t.Errorf("Tail = %+v, %v", tl, err)
	}
	sr, err := c.Search(ctx, a, "site/current/app.log", 0, "error", 10)
	if err != nil || !slices.Equal(sr.Matches, []Line{{Offset: 11, Text: "an ERROR here"}}) || sr.Next != 25 || sr.Stopped != "end" {
		t.Errorf("Search = %+v, %v", sr, err)
	}
	// site, current, releases, one and app.log; none world-writable.
	sc, err := c.ScanWorldWritable(ctx, a, "site")
	if err != nil || sc.Count != 0 || sc.Scanned != 5 || sc.Truncated {
		t.Errorf("ScanWorldWritable = %+v, %v", sc, err)
	}
	must(c.ExtractTar(ctx, a, "site/releases/two", tarball(t, tarDir("public/"), tarFile("public/index.php", "<?php echo 1;")),
		ExtractLimits{MaxBytes: 1 << 20, MaxEntries: 10}))
	idx, err := c.Stat(ctx, a, "site/releases/two/public/index.php")
	if err != nil || idx.Size != 13 || idx.Mode != 0o640 {
		t.Errorf("extracted index.php = %+v, %v", idx, err)
	}
	must(c.RemoveTree(ctx, a, "."))
	if l, err := c.List(ctx, a, "."); err != nil || len(l.Entries) != 0 {
		t.Errorf("after RemoveTree, the home lists %+v, %v", l, err)
	}
}

func TestClientNotExist(t *testing.T) {
	c, a := testClient(t)
	ctx := context.Background()
	_, err := c.Stat(ctx, a, "missing")
	if !errors.Is(err, fs.ErrNotExist) || !strings.Contains(err.Error(), "as-user stat as s_test: ") {
		t.Errorf("Stat(missing): %v, want fs.ErrNotExist from as-user stat as s_test", err)
	}
	if _, err := c.Read(ctx, a, "missing", 0, 10); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Read(missing): %v, want fs.ErrNotExist", err)
	}
}

func TestClientHelperError(t *testing.T) {
	c, a := testClient(t)
	ctx := context.Background()
	_, err := c.Read(ctx, a, "../escape", 0, 10)
	if err == nil || errors.Is(err, fs.ErrNotExist) || !strings.Contains(err.Error(), "as-user read as s_test: ") {
		t.Errorf("Read(../escape): %v, want the helper's error", err)
	}
	if err := c.run(ctx, a, "no-such-op", nil, &struct{}{}); err == nil || !strings.Contains(err.Error(), "usage") {
		t.Errorf("an unknown operation: %v, want the helper's usage", err)
	}
}

func TestClientChecksHomeOwner(t *testing.T) {
	c, a := testClient(t)
	a.Home = "/" // root's, not this user's
	if _, err := c.Stat(context.Background(), a, "."); err == nil || !strings.Contains(err.Error(), "not owned") {
		t.Errorf("Stat in a home owned by root: %v, want a refusal", err)
	}
}

func TestClientOutputLimits(t *testing.T) {
	c, a := testClient(t)
	for _, op := range []string{"test-flood", "test-two-lines", "test-silent"} {
		if err := c.run(context.Background(), a, op, nil, &struct{}{}); err == nil {
			t.Errorf("%s: no error", op)
		}
	}
}

func TestClientRefusesRootAccount(t *testing.T) {
	// The binary doesn't exist: the refusal has to come before any start.
	c := &Client{AgentBinary: "/nonexistent/zimam-agent"}
	for _, a := range []Account{
		{Name: "a", UID: 0, GID: 1000, Home: "/home/a"},
		{Name: "b", UID: 1000, GID: 0, Home: "/home/b"},
		{Name: "c", UID: 1000, GID: 1000, Groups: []uint32{1000, 0}, Home: "/home/c"},
	} {
		if err := c.RemoveTree(context.Background(), a, "."); err == nil || !strings.Contains(err.Error(), "refusing") {
			t.Errorf("%+v: %v, want a refusal", a, err)
		}
	}
}
