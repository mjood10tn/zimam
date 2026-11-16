package config

import (
	"os"
	"strings"
	"testing"

	"zimam/agent/internal/paths"
)

// A real ssh-ed25519 public key (GitHub's), as an e2e Git host's would be.
const testHostKey = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOMqqnkVzrm0SdG6UOoqKLsabgH5C9okWi0dh2l9GKJl"

func testGitHost() *E2EGitHost {
	return &E2EGitHost{Address: "10.41.169.50", Port: 22, HostKeys: []string{testHostKey}}
}

// writeWithGitHost saves a valid agent.json, then adds e2e_git_host to it
// by hand, as the e2e harness does, and returns the paths.
func writeWithGitHost(t *testing.T) paths.Paths {
	t.Helper()
	p := paths.Paths{Root: t.TempDir()}
	if err := Save(p, testAgent()); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(p.AgentConfig())
	if err != nil {
		t.Fatal(err)
	}
	host := `"e2e_git_host": {"address": "10.41.169.50", "port": 22, "host_keys": ["` + testHostKey + `"]},`
	data = []byte(strings.Replace(string(data), "{", "{\n  "+host, 1))
	if err := os.WriteFile(p.AgentConfig(), data, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestE2EGitHostCheck(t *testing.T) {
	if err := testGitHost().check(); err != nil {
		t.Fatalf("a valid Git host: %v", err)
	}
	v6 := testGitHost()
	v6.Address, v6.Port = "fd42::1", 2222
	if err := v6.check(); err != nil {
		t.Errorf("an IPv6 Git host on 2222: %v", err)
	}
	bad := map[string]func(h *E2EGitHost){
		"a host name":           func(h *E2EGitHost) { h.Address = "githost" },
		"a shell fragment":      func(h *E2EGitHost) { h.Address = "10.0.0.1;id" },
		"an address with space": func(h *E2EGitHost) { h.Address = "10.0.0.1 " },
		"a zone":                func(h *E2EGitHost) { h.Address = "fe80::1%eth0" },
		"a non-canonical v6":    func(h *E2EGitHost) { h.Address = "fd42:0::1" },
		"no address":            func(h *E2EGitHost) { h.Address = "" },
		"port 0":                func(h *E2EGitHost) { h.Port = 0 },
		"port 65536":            func(h *E2EGitHost) { h.Port = 65536 },
		"no keys":               func(h *E2EGitHost) { h.HostKeys = nil },
		"four keys":             func(h *E2EGitHost) { h.HostKeys = []string{testHostKey, testHostKey, testHostKey, testHostKey} },
		"a comment":             func(h *E2EGitHost) { h.HostKeys = []string{testHostKey + " root@githost"} },
		"a newline":             func(h *E2EGitHost) { h.HostKeys = []string{testHostKey + "\ngithub.com " + testHostKey} },
		"an unknown type":       func(h *E2EGitHost) { h.HostKeys = []string{"ssh-dss AAAAB3NzaC1kc3M="} },
		"a mislabelled blob":    func(h *E2EGitHost) { h.HostKeys = []string{"ssh-rsa" + testHostKey[len("ssh-ed25519"):]} },
		"bad base64":            func(h *E2EGitHost) { h.HostKeys = []string{"ssh-ed25519 AAAA*"} },
	}
	for name, mutate := range bad {
		h := testGitHost()
		mutate(h)
		if err := h.check(); err == nil {
			t.Errorf("%s: accepted %+v", name, *h)
		}
	}
}
