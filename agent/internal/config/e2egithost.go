package config

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net/netip"
	"regexp"
)

// E2EGitHost points deploys at the end-to-end run's own Git host instead of
// GitHub (e2e/githost). git still fetches git@github.com:<owner>/<name>.git;
// ssh connects to Address and Port and checks HostKeys under the name
// github.com (deploy's githost_e2e.go). Only e2e builds accept it: a release
// build refuses an agent.json that has it (e2egithost_release.go), so a
// production server can never be pointed at another host's keys.
type E2EGitHost struct {
	// Address is an IP literal, never a name: it ends up in the SSH command
	// git runs through /bin/sh.
	Address  string   `json:"address"`
	Port     int      `json:"port"`
	HostKeys []string `json:"host_keys"`
}

// hostKey is one public host key, "<type> <base64>", as ssh-keyscan and
// /etc/ssh/ssh_host_*_key.pub print it, without the comment.
var hostKey = regexp.MustCompile(`^(ssh-ed25519|ecdsa-sha2-nistp256|ssh-rsa) [A-Za-z0-9+/]+={0,2}\z`)

// check is the e2e build's validation: a plain IP address, a port, and
// one to three well-formed host keys.
func (h E2EGitHost) check() error {
	addr, err := netip.ParseAddr(h.Address)
	if err != nil || addr.Zone() != "" || addr.String() != h.Address {
		return errors.New("address must be a plain IP address")
	}
	if h.Port < 1 || h.Port > 65535 {
		return errors.New("port must be from 1 to 65535")
	}
	if len(h.HostKeys) < 1 || len(h.HostKeys) > 3 {
		return errors.New("host_keys must hold one to three keys")
	}
	for i, k := range h.HostKeys {
		m := hostKey.FindStringSubmatch(k)
		if m == nil {
			return fmt.Errorf("host_keys[%d] must be \"<type> <base64>\"", i)
		}
		blob, err := base64.StdEncoding.Strict().DecodeString(k[len(m[1])+1:])
		// The key's blob starts with its type, as a length-prefixed string.
		if err != nil || len(blob) < 4+len(m[1]) || string(blob[4:4+len(m[1])]) != m[1] {
			return fmt.Errorf("host_keys[%d] isn't a %s key", i, m[1])
		}
	}
	return nil
}
