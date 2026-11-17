//go:build roottest

package valkey

import (
	"bufio"
	"context"
	"crypto/sha256"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"
)

// This test runs against a real Valkey, which it installs when the machine has
// none: a throwaway container, with ZIMAM_ROOTTEST_VALKEY=1, as
// go test -tags roottest -run TestRootSiteUsers ./internal/valkey. Logins of
// a site's user give the password in REDISCLI_AUTH, or over a socket, never in
// argv.

const (
	passA1 = "Aa1_Qk3wL9pT7vXz2RmN4sYb8DeFgHjKm"
	passA2 = "Bb2-Zr5hC1uJ6eGa0VfK9tOq3WyNcXsPd"
	passA3 = "Cc3_Mn8bV4xQ7zL1kJ5hG2fDsAqWeRtYu"
	passB1 = "Dd4-Pl0oI9uY8tR7eW6qA5sD4fG3hJ2kL"
)

// conn is a client with one open connection to Valkey, logged in as a site's
// user, as a PHP worker is.
type conn struct {
	c net.Conn
	r *bufio.Reader
}

func dialAs(t *testing.T, user, pass string) *conn {
	t.Helper()
	c, err := net.Dial("tcp", "127.0.0.1:6379")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	cn := &conn{c: c, r: bufio.NewReader(c)}
	if out, err := cn.do("AUTH", user, pass); err != nil || out != "+OK" {
		t.Fatalf("AUTH as %s: %q, %v", user, out, err)
	}
	return cn
}

// do sends one command and returns the first line of the reply.
func (c *conn) do(args ...string) (string, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "*%d\r\n", len(args))
	for _, a := range args {
		fmt.Fprintf(&b, "$%d\r\n%s\r\n", len(a), a)
	}
	_ = c.c.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err := c.c.Write([]byte(b.String())); err != nil {
		return "", err
	}
	line, err := c.r.ReadString('\n')
	return strings.TrimRight(line, "\r\n"), err
}

func TestRootSiteUsers(t *testing.T) {
	d := rootDeps(t)
	ctx := context.Background()
	var st states

	if ok, err := Installed(d); err != nil {
		t.Fatal(err)
	} else if !ok {
		if _, err := Install(ctx, d, st.users); err != nil {
			t.Fatalf("Install: %v", err)
		}
	}
	admin := func(args ...string) string {
		t.Helper()
		out, err := cliAdmin(ctx, d, args...)
		if err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		return out
	}
	users := func() string { return strings.Join(strings.Fields(admin("ACL", "USERS")), " ") }
	apply := func(change func() (func() error, error)) error { return ApplyUsers(ctx, d, st.users, change) }

	if err := apply(st.setting("alpha", passA1)); err != nil {
		t.Fatal(err)
	}
	if err := apply(st.setting("beta", passB1)); err != nil {
		t.Fatal(err)
	}
	if got := shell(t, "/usr/bin/stat", "-c", "%U:%G %a", d.Paths.ValkeyACL()); got != "root:valkey 640" {
		t.Errorf("users.acl is %s, want root:valkey 640", got)
	}
	if got := users(); got != "default s_alpha s_beta zimam-admin" {
		t.Fatalf("the live users are %q", got)
	}
	admin("SET", "s_beta:x", "b's value")
	admin("SET", "s_alpha:y", "a's value")

	// A site's user reaches its own prefix and nothing else.
	t.Run("TestSiteUserConfinedToPrefix", func(t *testing.T) {
		as := func(user, pass string, args ...string) string {
			out := asUser(user, pass, args...)
			t.Logf("as %s: %s -> %s", user, strings.Join(args, " "), strings.ReplaceAll(out, "\n", " / "))
			return out
		}
		if out := as("s_alpha", passA1, "SET", "s_alpha:x", "1"); out != "OK" {
			t.Errorf("SET s_alpha:x = %q, want OK", out)
		}
		if out := as("s_alpha", passA1, "GET", "s_alpha:x"); out != "1" {
			t.Errorf("GET s_alpha:x = %q", out)
		}
		for _, c := range []struct {
			args []string
			want string
		}{
			{[]string{"GET", "s_beta:x"}, "NOPERM No permissions to access a key"},
			{[]string{"CONFIG", "GET", "*"}, "NOPERM"},
			{[]string{"FLUSHALL"}, "NOPERM"},
			{[]string{"FLUSHDB"}, "NOPERM"},
			{[]string{"SCRIPT", "FLUSH"}, "NOPERM"},
			{[]string{"SCRIPT", "KILL"}, "NOPERM"},
			{[]string{"SCRIPT", "DEBUG", "NO"}, "NOPERM"},
			{[]string{"KEYS", "*"}, "NOPERM"},
			{[]string{"ACL", "LIST"}, "NOPERM"},
			{[]string{"FUNCTION", "LOAD", "#!lua name=mylib\nredis.register_function('f', function() return 1 end)"}, "NOPERM"},
			{[]string{"EVAL", "return redis.call('GET', KEYS[1])", "1", "s_beta:x"}, "NOPERM No permissions to access a key"},
			{[]string{"EVAL", "return redis.call('GET', 's_beta:x')", "0"}, "ERR ACL failure in script"},
			{[]string{"PUBLISH", "s_beta:c", "m"}, "NOPERM No permissions to access a channel"},
			{[]string{"CLIENT", "KILL", "USER", "s_beta"}, "NOPERM"},
			{[]string{"INFO", "server"}, "NOPERM"},
		} {
			if out := as("s_alpha", passA1, c.args...); !strings.Contains(out, c.want) {
				t.Errorf("%v = %q, want it to contain %q", c.args, out, c.want)
			}
		}
		// And the other way round.
		if out := as("s_beta", passB1, "GET", "s_alpha:x"); !strings.Contains(out, "NOPERM") {
			t.Errorf("s_beta read s_alpha:x: %q", out)
		}
		if out := as("s_beta", passB1, "GET", "s_beta:x"); out != "b's value" {
			t.Errorf("s_beta read its own key: %q", out)
		}
		// Neither site's password opens the other's login.
		if out := as("s_alpha", passB1, "PING"); !strings.Contains(out, "WRONGPASS") {
			t.Errorf("s_alpha with s_beta's password: %q", out)
		}
		// Not hidden, and not claimed to be: key names show through SCAN.
		out := as("s_alpha", passA1, "SCAN", "0", "MATCH", "*")
		t.Logf("known limit: SCAN as s_alpha lists %q", out)
	})

	// A password change keeps open connections on the old password until they
	// are closed; the kill closes them, and the new password then works.
	t.Run("rotation", func(t *testing.T) {
		old := dialAs(t, "s_alpha", passA1)
		if err := apply(st.setting("alpha", passA2)); err != nil {
			t.Fatal(err)
		}
		out, err := old.do("PING")
		t.Logf("after the load, before the kill, the open connection answered %q, %v", out, err)
		if err != nil || out != "+PONG" {
			t.Errorf("the connection opened with the old password did not survive the load (%q, %v); the kill would be unneeded", out, err)
		}
		n, err := KillUserConnections(ctx, d, "alpha")
		if err != nil || n < 1 {
			t.Fatalf("KillUserConnections = %d, %v", n, err)
		}
		t.Logf("closed %d connection(s)", n)
		if out, err := old.do("PING"); err == nil {
			t.Errorf("the connection opened with the old password still answers %q", out)
		}
		if out := asUser("s_alpha", passA1, "PING"); !strings.Contains(out, "WRONGPASS") {
			t.Errorf("the old password gave %q, want WRONGPASS", out)
		}
		if out := asUser("s_alpha", passA2, "GET", "s_alpha:x"); out != "1" {
			t.Errorf("the new password gave %q; the site's keys must stay", out)
		}
		// A user the server hasn't got is no error to kill: the wording Valkey
		// uses must match the one the code expects.
		if n, err := KillUserConnections(ctx, d, "nosuchsite"); err != nil || n != 0 {
			t.Errorf("KillUserConnections of an unknown user = %d, %v", n, err)
		}
	})

	// A file Valkey refuses is put back and loaded again; the users keep their
	// passwords and the state goes back.
	t.Run("failed load", func(t *testing.T) {
		before := readFile(t, d.Paths.ValkeyACL())
		hashBefore := st.hash["alpha"]
		open := dialAs(t, "s_alpha", passA2)
		real := renderUsers
		renderUsers = func(admin [sha256.Size]byte, users []SiteUser) ([]byte, error) {
			good, err := real(admin, users)
			return append(good, "user s_broken on #nothex resetkeys\n"...), err
		}
		err := apply(st.setting("alpha", passA3))
		renderUsers = real
		if err == nil {
			t.Fatal("a file Valkey refuses was accepted")
		}
		t.Logf("the error: %v", err)
		if !strings.Contains(err.Error(), "put back") || !strings.Contains(err.Error(), "ACL LOAD") {
			t.Errorf("the error doesn't say what was restored: %v", err)
		}
		if got := readFile(t, d.Paths.ValkeyACL()); got != before {
			t.Errorf("users.acl is not the previous file:\n%s", got)
		}
		if st.hash["alpha"] != hashBefore {
			t.Error("the state kept the new hash")
		}
		if got := users(); got != "default s_alpha s_beta zimam-admin" {
			t.Errorf("the live users are %q", got)
		}
		if out := asUser("s_alpha", passA2, "GET", "s_alpha:x"); out != "1" {
			t.Errorf("s_alpha with its password gave %q", out)
		}
		if out := asUser("s_alpha", passA3, "PING"); !strings.Contains(out, "WRONGPASS") {
			t.Errorf("the refused password gave %q", out)
		}
		out, err2 := open.do("PING")
		t.Logf("a connection open across the failed load and the restore answered %q, %v", out, err2)
	})

	// Removing a site's user closes its connections by itself, and the rest of
	// the file stays.
	t.Run("destroy", func(t *testing.T) {
		open := dialAs(t, "s_beta", passB1)
		remove := func() (func() error, error) {
			old := st.hash["beta"]
			delete(st.hash, "beta")
			return func() error { st.hash["beta"] = old; return nil }, nil
		}
		if err := apply(remove); err != nil {
			t.Fatal(err)
		}
		if got := users(); got != "default s_alpha zimam-admin" {
			t.Errorf("the live users are %q", got)
		}
		if strings.Contains(readFile(t, d.Paths.ValkeyACL()), "s_beta") {
			t.Error("users.acl still has s_beta")
		}
		if out, err := open.do("PING"); err == nil {
			t.Errorf("the removed user's connection still answers %q", out)
		}
		if out := asUser("s_beta", passB1, "PING"); !strings.Contains(out, "WRONGPASS") {
			t.Errorf("the removed user's password gave %q", out)
		}
		if n, err := KillUserConnections(ctx, d, "beta"); err != nil || n != 0 {
			t.Errorf("KillUserConnections of the removed user = %d, %v", n, err)
		}

		// A site made again under the name has no user until one is given.
		if err := apply(nil); err != nil {
			t.Fatal(err)
		}
		if got := users(); got != "default s_alpha zimam-admin" {
			t.Errorf("after the render with no change the live users are %q", got)
		}
		if out := asUser("s_beta", passB1, "PING"); !strings.Contains(out, "WRONGPASS") {
			t.Errorf("the recreated site logged in: %q", out)
		}
		if err := apply(st.setting("beta", passA3)); err != nil {
			t.Fatal(err)
		}
		if out := asUser("s_beta", passA3, "PING"); out != "PONG" {
			t.Errorf("the new user: %q", out)
		}
		// ApplyUsers removes only the user: a site's keys are removed by
		// RemoveSiteUser, which site.destroy uses (see TestRootDestroyPurgesKeys).
		t.Logf("a site made again under a removed name sees its old key s_beta:x: %q", asUser("s_beta", passA3, "GET", "s_beta:x"))
	})

	// valkey.install renders from the same states, so a re-run keeps every
	// site's user and doesn't restart the server.
	t.Run("install keeps the users", func(t *testing.T) {
		pid := mainPID(t)
		if _, err := Install(ctx, d, st.users); err != nil {
			t.Fatal(err)
		}
		if got := users(); got != "default s_alpha s_beta zimam-admin" {
			t.Errorf("the live users are %q", got)
		}
		if out := asUser("s_alpha", passA2, "GET", "s_alpha:x"); out != "1" {
			t.Errorf("s_alpha after the install: %q", out)
		}
		if now := mainPID(t); now != pid {
			t.Errorf("a re-run that changed nothing restarted Valkey: pid %s, then %s", pid, now)
		}
	})
}

// A destroyed site leaves no key and no connection, another site whose name
// starts the same is untouched, and a site made again under the name reads
// nothing old. Key names with a space and a line break are written by the
// admin, which is how they could break a purge that read names from
// valkey-cli's output.
func TestRootDestroyPurgesKeys(t *testing.T) {
	d := rootDeps(t)
	ctx := context.Background()
	var st states
	if ok, err := Installed(d); err != nil {
		t.Fatal(err)
	} else if !ok {
		if _, err := Install(ctx, d, st.users); err != nil {
			t.Fatalf("Install: %v", err)
		}
	}
	admin := func(args ...string) string {
		t.Helper()
		out, err := cliAdmin(ctx, d, args...)
		if err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		return out
	}
	if err := ApplyUsers(ctx, d, st.users, st.setting("shop", passA1)); err != nil {
		t.Fatal(err)
	}
	if err := ApplyUsers(ctx, d, st.users, st.setting("shop-x", passB1)); err != nil {
		t.Fatal(err)
	}

	odd := []string{"s_shop:plain", "s_shop:with space", "s_shop:line\nbreak", "s_shop:quote\"it's", "s_shop:tab\tand\\slash", "s_shop:é-ünï", "s_shop:", "s_shop:*star", "s_shop:[x]"}
	for _, k := range odd {
		admin("SET", k, "old")
	}
	others := []string{"s_shop-x:plain", "s_shop-x:with space", "s_shop-x:line\nbreak", "s_shopx:a", "s_sho:a", "other:s_shop:a"}
	for _, k := range others {
		admin("SET", k, "keep")
	}
	if out := asUser("s_shop", passA1, "SET", "s_shop:written-by-site", "1"); out != "OK" {
		t.Fatalf("the site's SET: %q", out)
	}
	open := dialAs(t, "s_shop", passA1)

	// Timing: 50,000 keys of the site.
	t.Run("50000 keys", func(t *testing.T) {
		admin("EVAL", "for i = 1, 50000 do redis.call('SET', 's_shop:bulk:' .. i, i) end return 1", "0")
		start := time.Now()
		n, err := PurgeSiteKeys(ctx, d, "shop")
		took := time.Since(start)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("purged %d keys (50,000 bulk + %d others of the site) in %s", n, len(odd)+1, took)
		if n != 50000+len(odd)+1 {
			t.Errorf("purged %d, want %d", n, 50000+len(odd)+1)
		}
		// Put the site's odd keys back for the destroy below.
		for _, k := range odd {
			admin("SET", k, "old")
		}
		admin("SET", "s_shop:written-by-site", "1")
	})

	var prog Progress
	if err := RemoveSiteUser(ctx, d, st.users, "shop", memRemoval(&st, "shop", &prog)); err != nil {
		t.Fatalf("RemoveSiteUser = %v", err)
	}
	if _, has := st.hash["shop"]; has || prog != (Progress{}) {
		t.Errorf("the hash was not cleared: %v, progress %v", has, prog)
	}
	if got := admin("EVAL", "return #redis.call('KEYS', ARGV[1])", "0", "s_shop:*"); got != "0" {
		t.Errorf("%s keys s_shop:* remain", got)
	}
	for _, k := range append(append([]string{}, others...), "s_shop-x:plain") {
		if got := admin("EXISTS", k); got != "1" {
			t.Errorf("the key %q of another site is gone (EXISTS %s)", k, got)
		}
	}
	if got := admin("EVAL", "return #redis.call('KEYS', ARGV[1])", "0", "s_shop-x:*"); got != "3" {
		t.Errorf("s_shop-x:* has %s keys, want 3", got)
	}
	if out, err := open.do("PING"); err == nil {
		t.Errorf("the site's connection is still open: %q", out)
	}
	if out := asUser("s_shop", passA1, "PING"); !strings.Contains(out, "WRONGPASS") {
		t.Errorf("the removed user logged in: %q", out)
	}
	if !strings.Contains(admin("ACL", "USERS"), "s_shop-x") || strings.Contains(strings.ReplaceAll(admin("ACL", "USERS"), "s_shop-x", ""), "s_shop") {
		t.Errorf("live users: %q", admin("ACL", "USERS"))
	}

	// A site made again under the name starts empty.
	if err := ApplyUsers(ctx, d, st.users, st.setting("shop", passA2)); err != nil {
		t.Fatal(err)
	}
	if out := asUser("s_shop", passA2, "GET", "s_shop:plain"); out != "" {
		t.Errorf("the new site read %q", out)
	}
	if out := asUser("s_shop", passA2, "SCAN", "0", "MATCH", "s_shop:*"); out != "0" {
		t.Errorf("the new site sees keys: %q", out)
	}
	if out := asUser("s_shop-x", passB1, "GET", "s_shop-x:plain"); out != "keep" {
		t.Errorf("the other site's key: %q", out)
	}
}
