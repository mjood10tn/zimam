package server

import (
	"net"
	"os"
	"path/filepath"
	"testing"
)

func TestPeerUID(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "s.sock")
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: sock, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	client, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	c, err := ln.AcceptUnix()
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	uid, err := peerUID(c)
	if err != nil || uid != uint32(os.Getuid()) {
		t.Errorf("peerUID = %d, %v; want %d", uid, err, os.Getuid())
	}
}

func TestAllowRootAndZimam(t *testing.T) {
	for _, tc := range []struct {
		zimamUID uint32
		exists   bool
		uid      uint32
		want     bool
	}{
		{zimamUID: 999, exists: true, uid: 0, want: true},
		{zimamUID: 999, exists: true, uid: 999, want: true},
		{zimamUID: 999, exists: true, uid: 1000, want: false},
		{zimamUID: 999, exists: false, uid: 0, want: true},
		{zimamUID: 999, exists: false, uid: 999, want: false},
	} {
		if got := AllowRootAndZimam(tc.zimamUID, tc.exists)(tc.uid); got != tc.want {
			t.Errorf("AllowRootAndZimam(%d, %t)(%d) = %t, want %t", tc.zimamUID, tc.exists, tc.uid, got, tc.want)
		}
	}
}
