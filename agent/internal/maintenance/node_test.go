package maintenance

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"testing"
	"time"

	"zimam/agent/internal/node"
	"zimam/agent/internal/sysexec"
)

// nodeKeys serves NodeSource's key at its URL and counts the downloads.
type nodeKeys struct {
	key  []byte
	hits int
}

func (n *nodeKeys) RoundTrip(r *http.Request) (*http.Response, error) {
	code, body := http.StatusNotFound, []byte("not found")
	if r.URL.String() == node.KeyURL {
		n.hits++
		code, body = http.StatusOK, n.key
	}
	return &http.Response{StatusCode: code, Status: http.StatusText(code), Body: io.NopCloser(bytes.NewReader(body)), Request: r}, nil
}

// installNode sets the NodeSource repository up for major 24, as node.install
// does, and makes downloads come from the returned server.
func (e *env) installNode() *nodeKeys {
	e.t.Helper()
	keys := &nodeKeys{key: mustRead(e.t, "../node/testdata/nodesource.asc")}
	e.d.HTTP = &http.Client{Transport: keys}
	d := e.d
	d.Run = sysexec.NewFakeRunner(aptUpdate)
	if err := node.EnsureRepo(context.Background(), d, "24"); err != nil {
		e.t.Fatalf("setting the repository up: %v", err)
	}
	keys.hits = 0
	return keys
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// D23 for NodeSource: like sury's, its keyring is downloaded again once a
// week, and only once Node is installed.
func TestMaintenanceRefreshesNodeKeyringWeekly(t *testing.T) {
	for _, tc := range []struct {
		name      string
		installed bool
		record    any // written to node-keyring-refresh.json; nil: no file
		refresh   bool
	}{
		{"node not installed", false, nil, false},
		{"never tried", true, nil, true},
		{"tried 6 days ago", true, map[string]any{"attempted_at": testNow.AddDate(0, 0, -6)}, false},
		{"tried 8 days ago", true, map[string]any{"attempted_at": testNow.AddDate(0, 0, -8)}, true},
	} {
		e := newEnv(t)
		var keys *nodeKeys
		if tc.installed {
			keys = e.installNode()
		} else {
			keys = &nodeKeys{key: mustRead(t, "../node/testdata/nodesource.asc")}
			e.d.HTTP = &http.Client{Transport: keys}
		}
		if tc.record != nil {
			writeJSON(t, e.d.Paths.NodeKeyringRefresh(), tc.record)
		}
		if err := e.maintain(); err != nil {
			t.Fatalf("%s: Run: %v", tc.name, err)
		}
		if (keys.hits > 0) != tc.refresh || keys.hits > 1 {
			t.Errorf("%s: downloaded the key %d times, want refresh = %v", tc.name, keys.hits, tc.refresh)
		}
		if got := e.ran("/usr/bin/apt-get"); got != tc.refresh {
			t.Errorf("%s: apt-get update ran = %v, want %v", tc.name, got, tc.refresh)
		}
		_, err := os.Stat(e.d.Paths.NodeKeyringRefresh())
		switch {
		case !tc.installed && err == nil:
			t.Errorf("%s: wrote a refresh record without Node", tc.name)
		case tc.refresh:
			var rec recordOnDisk
			if err := json.Unmarshal(readFile(t, e.d.Paths.NodeKeyringRefresh()), &rec); err != nil || !rec.AttemptedAt.Equal(testNow) || rec.Error != "" {
				t.Errorf("%s: record = %+v, %v; want attempted now, no error", tc.name, rec, err)
			}
		}
		// The sury record is its own file: a Node refresh never touches it.
		if _, err := os.Stat(e.d.Paths.KeyringRefresh()); err == nil {
			t.Errorf("%s: wrote sury's refresh record", tc.name)
		}
	}
}

// A failed Node refresh keeps the installed files and the run going.
func TestNodeKeyringRefreshFailureKeepsRunning(t *testing.T) {
	e := newEnv(t)
	keys := e.installNode()
	keys.key = []byte("not a key")
	before := readFile(t, e.d.Paths.NodeKeyring())
	if err := e.maintain(); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !bytes.Equal(readFile(t, e.d.Paths.NodeKeyring()), before) {
		t.Error("the installed keyring changed")
	}
	var rec recordOnDisk
	if err := json.Unmarshal(readFile(t, e.d.Paths.NodeKeyringRefresh()), &rec); err != nil || rec.Error == "" || !rec.AttemptedAt.Equal(testNow) {
		t.Errorf("record = %+v, %v; want the attempt and its error", rec, err)
	}
}

// A pending nodejs upgrade is counted like any package's, PHP's included:
// the pin gives the NodeSource candidate priority 900, so apt-get -s
// dist-upgrade lists it.
func TestMaintenanceReportsNodeUpdate(t *testing.T) {
	e := newEnv(t, fixed(sysexec.Script{Path: "/usr/bin/apt-get", Args: []string{"-s", "dist-upgrade"}, Result: sysexec.Result{Stdout: []byte(
		"Inst nodejs [26.12.0-1nodesource1] (26.13.1-1nodesource1 . nodistro:nodistro [amd64])\n" +
			"Conf nodejs (26.13.1-1nodesource1 . nodistro:nodistro [amd64])\n")}}))
	writeJSON(t, e.d.Paths.UpdatesCache(), map[string]any{"total": 0, "security": 0, "checked_at": testNow.Add(-2 * time.Hour)})
	if err := e.maintain(); err != nil {
		t.Fatalf("Run: %v", err)
	}
	u := readUpdates(t, e.d.Paths)
	if u.Total == nil || *u.Total != 1 || u.Security == nil || *u.Security != 0 {
		t.Errorf("counts = %+v, want 1 update, 0 security", u)
	}
}
