package server

import (
	"maps"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

// fakeEnv is the process environment for activated.
type fakeEnv map[string]string

func (e fakeEnv) get(k string) string { return e[k] }
func (e fakeEnv) unset(k string) error {
	delete(e, k)
	return nil
}

// listenerFD returns a new fd for ln's socket, owned by no *os.File, as
// systemd passes it; activated takes it over.
func listenerFD(t *testing.T, ln interface{ File() (*os.File, error) }) int {
	t.Helper()
	f, err := ln.File()
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	fd, err := unix.Dup(int(f.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	return fd
}

func TestActivationEnv(t *testing.T) {
	pid := os.Getpid()
	me := strconv.Itoa(pid)
	for _, tc := range []struct {
		name string
		env  fakeEnv
		want string // in the error
	}{
		{name: "not from systemd", env: fakeEnv{}, want: "LISTEN_PID"},
		{name: "another process", env: fakeEnv{"LISTEN_PID": "1", "LISTEN_FDS": "1"}, want: "LISTEN_PID"},
		{name: "two sockets", env: fakeEnv{"LISTEN_PID": me, "LISTEN_FDS": "2"}, want: "LISTEN_FDS"},
		{name: "no sockets", env: fakeEnv{"LISTEN_PID": me, "LISTEN_FDS": "0", "LISTEN_FDNAMES": ""}, want: "LISTEN_FDS"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env := maps.Clone(tc.env)
			ln, err := activated(env.get, env.unset, pid, listenFDsStart)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("activated = %v, %v; want an error naming %s", ln, err, tc.want)
			}
			if len(env) != 0 {
				t.Errorf("left %v set", env)
			}
		})
	}

	t.Run("unix socket", func(t *testing.T) {
		sock := filepath.Join(t.TempDir(), "s.sock")
		orig, err := net.ListenUnix("unix", &net.UnixAddr{Name: sock, Net: "unix"})
		if err != nil {
			t.Fatal(err)
		}
		defer orig.Close()
		env := fakeEnv{"LISTEN_PID": me, "LISTEN_FDS": "1", "LISTEN_FDNAMES": "zimam-agent.socket"}
		ln, err := activated(env.get, env.unset, pid, listenerFD(t, orig))
		if err != nil {
			t.Fatal(err)
		}
		defer ln.Close()
		if len(env) != 0 {
			t.Errorf("left %v set", env)
		}
		client, err := net.Dial("unix", sock)
		if err != nil {
			t.Fatal(err)
		}
		defer client.Close()
		c, err := ln.Accept()
		if err != nil {
			t.Fatalf("accepting on the activated listener: %v", err)
		}
		c.Close()
	})

	t.Run("tcp socket", func(t *testing.T) {
		orig, err := net.ListenTCP("tcp", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
		if err != nil {
			t.Fatal(err)
		}
		defer orig.Close()
		env := fakeEnv{"LISTEN_PID": me, "LISTEN_FDS": "1"}
		if ln, err := activated(env.get, env.unset, pid, listenerFD(t, orig)); err == nil {
			ln.Close()
			t.Fatal("activated accepted a TCP socket")
		}
	})
}
