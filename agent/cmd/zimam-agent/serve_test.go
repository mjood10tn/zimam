package main

import (
	"errors"
	"os/user"
	"strings"
	"testing"
)

// Outside systemd there's no socket, so serve stops before touching anything.
func TestServeNeedsSystemd(t *testing.T) {
	t.Setenv("LISTEN_PID", "")
	t.Setenv("LISTEN_FDS", "")
	code, _, stderr := runCapture([]string{"serve"})
	if code != 1 || !strings.Contains(stderr, "LISTEN_PID") {
		t.Errorf("serve = %d, %q; want 1 and a message about LISTEN_PID", code, stderr)
	}
}

func TestServeTakesNoArguments(t *testing.T) {
	code, _, stderr := runCapture([]string{"serve", "extra"})
	if code != 2 || !strings.Contains(stderr, "usage") {
		t.Errorf("serve extra = %d, %q; want 2 and usage", code, stderr)
	}
}

func TestZimamUID(t *testing.T) {
	lookup := func(u *user.User, err error) func(string) (*user.User, error) {
		return func(name string) (*user.User, error) {
			if name != "zimam" {
				t.Errorf("looked up %q, want zimam", name)
			}
			return u, err
		}
	}
	if uid, exists, err := zimamUID(lookup(&user.User{Uid: "1001"}, nil)); uid != 1001 || !exists || err != nil {
		t.Errorf("zimamUID = %d, %t, %v; want 1001, true, nil", uid, exists, err)
	}
	if _, exists, err := zimamUID(lookup(nil, user.UnknownUserError("zimam"))); exists || err != nil {
		t.Errorf("without zimam: exists %t, err %v; want false, nil (root only)", exists, err)
	}
	if _, _, err := zimamUID(lookup(nil, errors.New("reading /etc/passwd failed"))); err == nil {
		t.Error("a lookup failure must stop serve")
	}
	if _, _, err := zimamUID(lookup(&user.User{Uid: "x"}, nil)); err == nil {
		t.Error("a non-numeric uid must stop serve")
	}
}
