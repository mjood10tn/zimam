package valkey

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"zimam/agent/contract"
	"zimam/agent/internal/services"
	"zimam/agent/internal/sysexec"
)

var (
	loadOK   = live("OK\n", "ACL", "LOAD")
	loadFail = failing(live("", "ACL", "LOAD"), 1, "ERR /etc/zimam/valkey/users.acl:2: Error in applying operation '+@nosuchcat'. WARNING: ACL errors detected, no change to the previously active ACL rules was performed\n")
)

func hashOf(pw string) [sha256.Size]byte { return sha256.Sum256([]byte(pw)) }

// applyEnv is a server where Valkey is installed: admin password, the users
// file with no site users, and the lock directory.
func applyEnv(t *testing.T) *testEnv {
	t.Helper()
	e := cliEnv(t)
	acl, err := RenderACL(hashOf(testPass), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(e.p.ValkeyACL(), acl, 0o640); err != nil {
		t.Fatal(err)
	}
	return e
}

// states is a stand-in for the site state files: what the users loader
// reads and what the change writes.
type states struct {
	mu   sync.Mutex
	hash map[string]string // site name to the password's SHA-256, hex
}

func (s *states) set(name, pw string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.hash == nil {
		s.hash = map[string]string{}
	}
	h := hashOf(pw)
	s.hash[name] = hex.EncodeToString(h[:])
}

func (s *states) users() ([]SiteUser, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []SiteUser
	for name, h := range s.hash {
		b, err := ParseHash(h)
		if err != nil {
			return nil, err
		}
		out = append(out, SiteUser{Name: contractName(name), Hash: b})
	}
	return out, nil
}

// setting is a change that sets one site's hash and puts the old one back.
func (s *states) setting(name, pw string) func() (func() error, error) {
	return func() (func() error, error) {
		s.mu.Lock()
		old, had := s.hash[name]
		s.mu.Unlock()
		s.set(name, pw)
		return func() error {
			s.mu.Lock()
			defer s.mu.Unlock()
			if had {
				s.hash[name] = old
			} else {
				delete(s.hash, name)
			}
			return nil
		}, nil
	}
}

func userLine(name, pw string) string {
	h := hashOf(pw)
	return "user s_" + name + " on #" + hex.EncodeToString(h[:]) + " resetkeys ~s_" + name + ":* resetchannels &s_" + name + ":* +@all -@admin -@dangerous -function\n"
}

func TestACLRenderedFromStates(t *testing.T) {
	e := applyEnv(t)
	e.add(loadOK)
	var st states
	st.set("shop", "shop password")
	if err := ApplyUsers(context.Background(), e.d, st.users, st.setting("blog", "blog password")); err != nil {
		t.Fatal(err)
	}
	want := "user default off\n" +
		"user zimam-admin on #" + hex.EncodeToString(func() []byte { h := hashOf(testPass); return h[:] }()) + " ~* &* +@all\n" +
		userLine("blog", "blog password") + userLine("shop", "shop password")
	if got := readFile(t, e.p.ValkeyACL()); got != want {
		t.Errorf("users.acl =\n%s\nwant\n%s", got, want)
	}
	wantMode(t, e.p.ValkeyACL(), 0o640)
	if got, want := lines(e.run.Calls()), []string{line(loadOK)}; !slices.Equal(got, want) {
		t.Errorf("ran\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if c := e.run.Calls()[0]; !slices.Equal(c.Env, []string{"REDISCLI_AUTH=" + testPass}) {
		t.Errorf("ACL LOAD ran with env %q", c.Env)
	}
	if _, err := os.Lstat(e.p.ValkeyACL() + ".zimam-prev"); err == nil {
		t.Error("a .zimam-prev file is left")
	}
}

// A nil change re-renders from the states as they are: what valkey.install
// does with the same function.
func TestApplyWithoutAChange(t *testing.T) {
	e := applyEnv(t)
	e.add(loadOK)
	var st states
	st.set("shop", "pw")
	if err := ApplyUsers(context.Background(), e.d, st.users, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(readFile(t, e.p.ValkeyACL()), userLine("shop", "pw")) {
		t.Error("the shop's user is missing")
	}
}

func TestRemovingASiteRemovesItsLine(t *testing.T) {
	e := applyEnv(t)
	e.add(loadOK)
	var st states
	st.set("shop", "a")
	st.set("blog", "b")
	remove := func() (func() error, error) {
		delete(st.hash, "shop")
		return func() error { st.set("shop", "a"); return nil }, nil
	}
	if err := ApplyUsers(context.Background(), e.d, st.users, remove); err != nil {
		t.Fatal(err)
	}
	got := readFile(t, e.p.ValkeyACL())
	if strings.Contains(got, "s_shop") || !strings.Contains(got, userLine("blog", "b")) {
		t.Errorf("users.acl =\n%s", got)
	}
}

func TestFailedLoadRestoresPreviousFile(t *testing.T) {
	for name, tc := range map[string]struct {
		second   sysexec.Script
		wantText []string
	}{
		"the restored file loads": {loadOK, []string{"put back", "ACL LOAD", "no change to the previously active ACL rules"}},
		"it doesn't either":       {loadFail, []string{"may be running users that are not in", "put back"}},
	} {
		t.Run(name, func(t *testing.T) {
			e := applyEnv(t)
			before := readFile(t, e.p.ValkeyACL())
			e.add(loadFail, tc.second)
			var st states
			st.set("shop", "old")
			reverted := 0
			change := st.setting("blog", "new")
			wrapped := func() (func() error, error) {
				revert, err := change()
				return func() error { reverted++; return revert() }, err
			}
			err := ApplyUsers(context.Background(), e.d, st.users, wrapped)
			if err == nil {
				t.Fatal("ApplyUsers accepted a refused file")
			}
			for _, w := range tc.wantText {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("the error %q doesn't say %q", err, w)
				}
			}
			if got := readFile(t, e.p.ValkeyACL()); got != before {
				t.Errorf("users.acl =\n%s\nwant the previous file\n%s", got, before)
			}
			if reverted != 1 {
				t.Errorf("the change was reverted %d times, want once", reverted)
			}
			if _, ok := st.hash["blog"]; ok {
				t.Error("the state still holds the new user")
			}
			if got, want := lines(e.run.Calls()), []string{line(loadFail), line(loadFail)}; !slices.Equal(got, want) {
				t.Errorf("ran\n%s\nwant two ACL LOADs", strings.Join(got, "\n"))
			}
		})
	}
}

// A cancelled job still puts the previous users back and loads them.
func TestRestoreLoadSurvivesACancelledJob(t *testing.T) {
	e := applyEnv(t)
	before := readFile(t, e.p.ValkeyACL())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	loads := 0
	var secondErr error
	e.srv.onCall = func(c sysexec.Cmd, cctx context.Context) error {
		if c.Path != cliPath {
			return nil
		}
		loads++
		if loads == 1 {
			cancel()
			return context.Canceled
		}
		secondErr = cctx.Err()
		return nil
	}
	e.d.Run = e.srv
	e.add(loadOK)
	var st states
	err := ApplyUsers(ctx, e.d, st.users, st.setting("shop", "pw"))
	if err == nil {
		t.Fatal("ApplyUsers survived a cancel")
	}
	if loads != 2 {
		t.Fatalf("%d ACL LOADs, want the load and the restore's", loads)
	}
	if secondErr != nil {
		t.Errorf("the restore's load ran with a cancelled context: %v", secondErr)
	}
	if got := readFile(t, e.p.ValkeyACL()); got != before {
		t.Errorf("users.acl =\n%s\nwant the previous file", got)
	}
	if _, ok := st.hash["shop"]; ok {
		t.Error("the state still holds the new user")
	}
}

func TestChangeThatFailsChangesNothing(t *testing.T) {
	e := applyEnv(t)
	before := readFile(t, e.p.ValkeyACL())
	var st states
	boom := errors.New("state file is read-only")
	reverted := false
	err := ApplyUsers(context.Background(), e.d, st.users, func() (func() error, error) {
		return func() error { reverted = true; return nil }, boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("ApplyUsers = %v, want the change's error", err)
	}
	if reverted || len(e.run.Calls()) != 0 || readFile(t, e.p.ValkeyACL()) != before {
		t.Errorf("reverted=%v, %d runs, file changed=%v", reverted, len(e.run.Calls()), readFile(t, e.p.ValkeyACL()) != before)
	}
}

// The users can't be read after the change was made: it is put back and no
// file is written.
func TestUsersThatCantBeReadRevertTheChange(t *testing.T) {
	e := applyEnv(t)
	before := readFile(t, e.p.ValkeyACL())
	var st states
	broken := func() ([]SiteUser, error) { return nil, errors.New("shop's state is unreadable") }
	err := ApplyUsers(context.Background(), e.d, broken, st.setting("shop", "pw"))
	if err == nil || !strings.Contains(err.Error(), "shop's state is unreadable") {
		t.Fatalf("ApplyUsers = %v", err)
	}
	if _, ok := st.hash["shop"]; ok {
		t.Error("the change was not reverted")
	}
	if len(e.run.Calls()) != 0 || readFile(t, e.p.ValkeyACL()) != before {
		t.Error("something ran or was written")
	}
}

func TestRevertFailureIsReported(t *testing.T) {
	e := applyEnv(t)
	e.add(loadFail, loadOK)
	var st states
	err := ApplyUsers(context.Background(), e.d, st.users, func() (func() error, error) {
		return func() error { return errors.New("disk full") }, nil
	})
	if err == nil || !strings.Contains(err.Error(), "disk full") || !strings.Contains(err.Error(), "state") {
		t.Errorf("ApplyUsers = %v, want the revert's failure and that the state is out of step", err)
	}
}

// Without the admin password or the users file there is no Valkey of Zimam's
// to change; the state is left alone.
func TestApplyNeedsAnInstalledValkey(t *testing.T) {
	for name, remove := range map[string]func(*testEnv){
		"no admin password": func(e *testEnv) { os.Remove(e.p.ValkeyAdminPass()) },
		"no users file":     func(e *testEnv) { os.Remove(e.p.ValkeyACL()) },
	} {
		t.Run(name, func(t *testing.T) {
			e := applyEnv(t)
			remove(e)
			ran := false
			err := ApplyUsers(context.Background(), e.d, noUsers, func() (func() error, error) { ran = true; return nil, nil })
			if err == nil {
				t.Fatal("ApplyUsers worked without Valkey")
			}
			if ran || len(e.run.Calls()) != 0 {
				t.Errorf("the change ran (%v) or something was executed (%d)", ran, len(e.run.Calls()))
			}
		})
	}
}

// The change and the render are one hold of the lock: while one apply waits
// in its ACL LOAD, another one's change doesn't run, so no render is made from
// a state read before another's write. Both users are in the end.
func TestConcurrentUsersSerialised(t *testing.T) {
	e := applyEnv(t)
	e.add(loadOK)
	var st states
	inLoad, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	e.srv.onCall = func(c sysexec.Cmd, _ context.Context) error {
		if c.Path == cliPath {
			once.Do(func() {
				close(inLoad)
				<-release
			})
		}
		return nil
	}
	e.d.Run = e.srv
	var secondRan sync.WaitGroup
	secondRan.Add(1)
	secondChange := func() (func() error, error) {
		secondRan.Done()
		return st.setting("blog", "b")()
	}
	errs := make(chan error, 2)
	go func() { errs <- ApplyUsers(context.Background(), e.d, st.users, st.setting("shop", "a")) }()
	<-inLoad
	go func() { errs <- ApplyUsers(context.Background(), e.d, st.users, secondChange) }()
	started := make(chan struct{})
	go func() { secondRan.Wait(); close(started) }()
	select {
	case <-started:
		t.Fatal("the second change ran while the first apply held the lock")
	case <-time.After(300 * time.Millisecond):
	}
	close(release)
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	got := readFile(t, e.p.ValkeyACL())
	if !strings.Contains(got, userLine("shop", "a")) || !strings.Contains(got, userLine("blog", "b")) {
		t.Errorf("users.acl =\n%s\nwant both users", got)
	}
}

// valkey.install holds the same lock, so an apply waits for it and gives up
// with a message that says to try again.
func TestApplyWaitsForTheInstallLock(t *testing.T) {
	e := applyEnv(t)
	e.add(loadOK)
	unlock, err := sysexec.Lock(context.Background(), e.p.ValkeyACLLock())
	if err != nil {
		t.Fatal(err)
	}
	old := lockWait
	lockWait = 150 * time.Millisecond
	defer func() { lockWait = old }()
	ran := false
	var st states
	err = ApplyUsers(context.Background(), e.d, st.users, func() (func() error, error) { ran = true; return nil, nil })
	if err == nil || !strings.Contains(err.Error(), "try again") {
		t.Fatalf("ApplyUsers = %v, want a busy message", err)
	}
	if ran || len(e.run.Calls()) != 0 {
		t.Error("the change or a command ran without the lock")
	}
	unlock()
	if err := ApplyUsers(context.Background(), e.d, st.users, nil); err != nil {
		t.Errorf("after the lock is free: %v", err)
	}
}

// A file the render hook corrupts is what a real server refuses: used by the
// roottest, and here to see that the hook is the render's only way in.
func TestRenderHookIsWhatIsWritten(t *testing.T) {
	e := applyEnv(t)
	e.add(loadOK)
	old := renderUsers
	renderUsers = func(admin [sha256.Size]byte, users []SiteUser) ([]byte, error) { return []byte("user x\n"), nil }
	defer func() { renderUsers = old }()
	if err := ApplyUsers(context.Background(), e.d, noUsers, nil); err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, e.p.ValkeyACL()); got != "user x\n" {
		t.Errorf("users.acl = %q", got)
	}
}

func TestKillUserConnections(t *testing.T) {
	kill := func(out string) sysexec.Script { return live(out, "CLIENT", "KILL", "USER", "s_shop") }
	for name, tc := range map[string]struct {
		script sysexec.Script
		want   int
		err    string
	}{
		"one closed":             {kill("1\n"), 1, ""},
		"none open":              {kill("0\n"), 0, ""},
		"the user is gone":       {failing(kill(""), 1, "ERR No such user 's_shop'\n"), 0, ""},
		"another error":          {failing(kill(""), 1, "ERR something else\n"), 0, "something else"},
		"a reply that isn't one": {kill("OK\n"), 0, "OK"},
	} {
		t.Run(name, func(t *testing.T) {
			e := cliEnv(t)
			e.add(tc.script)
			n, err := KillUserConnections(context.Background(), e.d, "shop")
			if (err == nil) != (tc.err == "") || (err != nil && !strings.Contains(err.Error(), tc.err)) {
				t.Fatalf("err = %v, want %q", err, tc.err)
			}
			if n != tc.want {
				t.Errorf("closed %d, want %d", n, tc.want)
			}
		})
	}
	t.Run("a name that isn't a site's", func(t *testing.T) {
		e := cliEnv(t)
		for _, bad := range []string{"", "Shop", "a b", "x\nFLUSHALL", "-a"} {
			if _, err := KillUserConnections(context.Background(), e.d, contractName(bad)); err == nil {
				t.Errorf("accepted %q", bad)
			}
		}
		if len(e.run.Calls()) != 0 {
			t.Error("a command ran")
		}
	})
}

func TestRunningAndInstalled(t *testing.T) {
	e := cliEnv(t)
	if ok, err := Installed(e.d); err != nil || ok {
		t.Errorf("Installed on a bare server = %v, %v", ok, err)
	}
	if err := serviceRecord(e); err != nil {
		t.Fatal(err)
	}
	if ok, err := Installed(e.d); err != nil || !ok {
		t.Errorf("Installed = %v, %v", ok, err)
	}
	e.add(livePing)
	if err := Ping(context.Background(), e.d); err != nil {
		t.Errorf("Ping: %v", err)
	}
	e2 := cliEnv(t)
	e2.add(live("PONG2\n", "PING"))
	if err := Ping(context.Background(), e2.d); err == nil {
		t.Error("Ping accepted a reply that is not PONG")
	}
}

func contractName(s string) contract.SiteName { return contract.SiteName(s) }

func serviceRecord(e *testEnv) error {
	return services.Update(e.p, func(s *services.State) { s.Valkey = services.Entry{Installed: true, Version: "9.0.4"} })
}
