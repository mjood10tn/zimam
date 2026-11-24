// Package firewall manages the UFW rules Zimam owns: the ones whose comment
// is zimam:<id> (R06-D3). Rules it didn't make are listed, never changed.
package firewall

import (
	"bufio"
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"net/netip"
	"os"
	"slices"
	"strconv"
	"strings"

	"zimam/agent/contract"
	"zimam/agent/internal/paths"
	"zimam/agent/internal/sysexec"
)

const (
	ufw  = "/usr/sbin/ufw"
	sshd = "/usr/sbin/sshd"
)

// commentPrefix tags the rules Zimam owns.
const commentPrefix = "zimam:"

// tuple is one "### tuple ###" line of user.rules or user6.rules: UFW's own
// record of a rule, with its comment hex-encoded (spike, Step 1).
type tuple struct {
	Action string // allow, deny, limit, or UFW's others for external rules
	Proto  string // tcp, udp, any
	Port   string // "22", "9000:9010", "any"
	From   string // as UFW wrote it: "0.0.0.0/0", "::/0", "192.0.2.9", "203.0.113.0/24"
	IPv6   bool
	ID     contract.FirewallRuleID // "" when the comment isn't zimam:<id>
}

// anywhere reports a source of every address in its family.
func (t tuple) anywhere() bool { return t.From == "0.0.0.0/0" || t.From == "::/0" }

// readTuples reads the rules of user.rules and, when it exists, user6.rules.
func readTuples(p paths.Paths) ([]tuple, error) {
	var out []tuple
	for _, f := range []struct {
		path string
		v6   bool
	}{{p.UFWUserRules(), false}, {p.UFWUser6Rules(), true}} {
		b, err := os.ReadFile(f.path)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		out = append(out, parseTuples(b, f.v6)...)
	}
	return out, nil
}

// parseTuples reads "### tuple ### <action> <proto> <dport> <dst> <sport>
// <src> [<dapp> <sapp>] <direction> [comment=<hex>]". Only inbound rules
// count; Zimam manages no others.
func parseTuples(b []byte, v6 bool) []tuple {
	var out []tuple
	s := bufio.NewScanner(bytes.NewReader(b))
	for s.Scan() {
		rest, ok := strings.CutPrefix(s.Text(), "### tuple ### ")
		if !ok {
			continue
		}
		f := strings.Fields(rest)
		var comment string
		if n := len(f); n > 0 && strings.HasPrefix(f[n-1], "comment=") {
			c, err := hex.DecodeString(strings.TrimPrefix(f[n-1], "comment="))
			if err == nil {
				comment = string(c)
			}
			f = f[:n-1]
		}
		if len(f) < 7 || !strings.HasPrefix(f[len(f)-1], "in") {
			continue
		}
		t := tuple{Action: f[0], Proto: f[1], Port: f[2], From: f[5], IPv6: v6}
		if id, ok := strings.CutPrefix(comment, commentPrefix); ok && contract.FirewallRuleID(id).Validate() == nil {
			t.ID = contract.FirewallRuleID(id)
		}
		out = append(out, t)
	}
	return out
}

// ipv6Enabled reads IPV6= from /etc/default/ufw; UFW's default is yes.
func ipv6Enabled(path string) (bool, error) {
	on, err := readSetting(path, "IPV6")
	if errors.Is(err, fs.ErrNotExist) {
		return true, nil
	}
	return on, err
}

func readSetting(path, key string) (bool, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	for line := range strings.Lines(string(b)) {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), key+"="); ok {
			return strings.Trim(v, `"'`) == "yes", nil
		}
	}
	return false, nil
}

// spec is one UFW rule Zimam wants: a rule's ports and one of its sources
// (an invalid prefix means anywhere).
type spec struct {
	ID     contract.FirewallRuleID
	Action contract.FirewallAction
	Proto  contract.FirewallProto
	From   contract.TCPPort
	To     contract.TCPPort
	Source netip.Prefix
}

func (s spec) port() string {
	if s.From == s.To {
		return strconv.Itoa(int(s.From))
	}
	return strconv.Itoa(int(s.From)) + ":" + strconv.Itoa(int(s.To))
}

func (s spec) source() string {
	switch {
	case !s.Source.IsValid():
		return "any"
	case s.Source.IsSingleIP():
		return s.Source.Addr().String()
	}
	return s.Source.String()
}

// specs splits a rule into one spec per source.
func specs(r contract.FirewallRule) []spec {
	base := spec{ID: r.ID, Action: r.Action, Proto: r.Proto, From: r.PortFrom, To: r.PortTo}
	if len(r.From) == 0 {
		return []spec{base}
	}
	out := make([]spec, 0, len(r.From))
	for _, e := range r.From {
		p, err := contract.ParseIPEntry(e)
		if err != nil {
			continue // Validate refused it already
		}
		s := base
		s.Source = p
		out = append(out, s)
	}
	return out
}

// matches reports whether a tuple is this spec's rule in one family, the
// comment aside.
func (s spec) matches(t tuple) bool {
	if t.Action != string(s.Action) || t.Proto != string(s.Proto) || t.Port != s.port() {
		return false
	}
	if !s.Source.IsValid() {
		return t.anywhere()
	}
	p, err := contract.ParseIPEntry(t.From)
	return err == nil && p == s.Source
}

// samePlace reports whether a tuple has this spec's proto, ports and source,
// whatever its action: UFW keeps one rule there.
func (s spec) samePlace(t tuple) bool {
	u := s
	u.Action = contract.FirewallAction(t.Action)
	return u.matches(t)
}

// args is the spec as ufw arguments, each a separate argument and never
// through a shell (R06-D3). Deny rules go first in their family's list,
// since UFW stops at the first match and appends by default (spike); allow
// and limit rules are appended.
func (s spec) args() []string {
	a := []string{string(s.Action), "proto", string(s.Proto), "from", s.source(), "to", "any", "port", s.port(), "comment", commentPrefix + string(s.ID)}
	if s.Action == "deny" {
		return append([]string{"prepend"}, a...)
	}
	return a
}

// deleteArgs deletes the rule a tuple records. UFW matches by spec and
// ignores the comment; "from any" deletes both families.
func deleteArgs(t tuple) []string {
	from := t.From
	if t.anywhere() {
		from = "any"
	}
	return []string{"delete", t.Action, "proto", t.Proto, "from", from, "to", "any", "port", t.Port}
}

func runUFW(ctx context.Context, d sysexec.Deps, args ...string) error {
	_, err := d.Run.Run(ctx, sysexec.Cmd{Path: ufw, Args: args})
	return err
}

// PrepareSSHD makes the directory sshd -t and -T refuse to run without.
// Under socket activation it exists only while ssh.service runs.
func PrepareSSHD(d sysexec.Deps) error {
	return os.MkdirAll(d.Paths.SSHDRunDir(), 0o755)
}

// SSHPorts reads the ports sshd listens on from sshd -T, the effective
// configuration whichever drop-in set them.
func SSHPorts(ctx context.Context, d sysexec.Deps) ([]contract.TCPPort, error) {
	if err := PrepareSSHD(d); err != nil {
		return nil, err
	}
	res, err := d.Run.Run(ctx, sysexec.Cmd{Path: sshd, Args: []string{"-T"}})
	if err != nil {
		return nil, fmt.Errorf("reading sshd's ports: %w", err)
	}
	var ports []contract.TCPPort
	for line := range strings.Lines(string(res.Stdout)) {
		v, ok := strings.CutPrefix(strings.TrimSpace(line), "port ")
		if !ok {
			continue
		}
		n, err := strconv.Atoi(v)
		if err != nil || contract.TCPPort(n).Validate() != nil {
			return nil, fmt.Errorf("sshd -T gave port %q", v)
		}
		if !slices.Contains(ports, contract.TCPPort(n)) {
			ports = append(ports, contract.TCPPort(n))
		}
	}
	if len(ports) == 0 {
		return nil, errors.New("sshd -T gave no port")
	}
	return ports, nil
}
