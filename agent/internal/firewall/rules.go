package firewall

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"zimam/agent/contract"
	"zimam/agent/internal/changes"
	"zimam/agent/internal/configtx"
	"zimam/agent/internal/sysexec"
)

// Window is how long the owner has to confirm a firewall change (R06-D1,
// OQ2).
const Window = 120 * time.Second

func init() {
	changes.Register(changes.Firewall, changes.Hooks{Revert: Restore})
}

// webPorts are the ports Nginx serves; with the SSH ports they are the
// locked ports no rule may block from anywhere.
var webPorts = []contract.TCPPort{80, 443}

// CheckLockout refuses a deny or limit rule from anywhere that covers an SSH
// port, 80 or 443: it would cut off the owner or every site, and the
// contract can't check it because the SSH ports come from sshd -T.
func CheckLockout(ctx context.Context, d sysexec.Deps, rules []contract.FirewallRule) error {
	ssh, err := SSHPorts(ctx, d)
	if err != nil {
		return err
	}
	f := contract.FieldErrors{}
	for i, r := range rules {
		if r.Action == "allow" || r.Proto != "tcp" || len(r.From) > 0 {
			continue
		}
		for _, port := range append(slices.Clone(ssh), webPorts...) {
			if r.PortFrom <= port && port <= r.PortTo {
				f[fmt.Sprintf("rules.%d.action", i)] = fmt.Sprintf("would %s port %d from anywhere, which SSH or the sites need", r.Action, port)
			}
		}
	}
	return f.Err()
}

// lockedRules are the rules the agent keeps: one per SSH port, and http
// and https unless they exist already (Cloudflare mode narrows them, R06-D9).
func lockedRules(ssh []contract.TCPPort, tuples []tuple) []contract.FirewallRule {
	var out []contract.FirewallRule
	for _, port := range ssh {
		out = append(out, contract.FirewallRule{ID: contract.FirewallRuleID("ssh-" + strconv.Itoa(int(port))), Action: "allow", Proto: "tcp", PortFrom: port, PortTo: port, From: contract.IPList{}})
	}
	for _, r := range webRules(contract.IPList{}) {
		if !slices.ContainsFunc(tuples, func(t tuple) bool { return t.ID == r.ID }) {
			out = append(out, r)
		}
	}
	return out
}

// webRules are the locked http and https rules, allowing from these sources
// (anywhere when empty).
func webRules(from contract.IPList) []contract.FirewallRule {
	return []contract.FirewallRule{
		{ID: "http", Action: "allow", Proto: "tcp", PortFrom: 80, PortTo: 80, From: from},
		{ID: "https", Action: "allow", Proto: "tcp", PortFrom: 443, PortTo: 443, From: from},
	}
}

// isWebRule reports a tuple of the http or https rule. An untagged "allow
// 80 (or 443) from anywhere" counts too: it is the installer's rule not yet
// adopted, and left in place it would keep the port open to everyone.
func isWebRule(t tuple) bool {
	if t.ID == "http" || t.ID == "https" {
		return true
	}
	return t.ID == "" && t.Action == "allow" && t.Proto == "tcp" && (t.Port == "80" || t.Port == "443") && t.anywhere()
}

// WebRestricted reports whether the http or https rule allows less than
// anywhere: Cloudflare's origin restriction is on (R06-D9).
func WebRestricted(d sysexec.Deps) (bool, error) {
	tuples, err := readTuples(d.Paths)
	if err != nil {
		return false, err
	}
	return slices.ContainsFunc(tuples, func(t tuple) bool { return (t.ID == "http" || t.ID == "https") && !t.anywhere() }), nil
}

// WebSources makes the http and https rules allow from these sources, or
// from anywhere when from is empty (R06-D9). It doesn't guard: cloudflare.mode
// runs it inside Guarded, and a range refresh runs it as it is.
func WebSources(ctx context.Context, d sysexec.Deps, from contract.IPList) error {
	tuples, err := readTuples(d.Paths)
	if err != nil {
		return err
	}
	v6, err := ipv6Enabled(d.Paths.UFWDefaults())
	if err != nil {
		return err
	}
	calls, err := webCalls(from, tuples, v6)
	if err != nil {
		return err
	}
	return runCalls(ctx, d, calls)
}

// webCalls adds the new web rules before deleting the old ones, so the sites
// never lose their ports.
func webCalls(from contract.IPList, tuples []tuple, v6 bool) ([][]string, error) {
	want := webRules(from)
	calls, err := adds(want, tuples, v6)
	if err != nil {
		return nil, err
	}
	wanted := allSpecs(want)
	for _, t := range tuples {
		// An untagged rule a wanted spec matches was just adopted by adds.
		if isWebRule(t) && !slices.ContainsFunc(wanted, func(s spec) bool { return (s.ID == t.ID || t.ID == "") && s.matches(t) }) {
			calls = appendOnce(calls, deleteArgs(t))
		}
	}
	return calls, nil
}

// plan is the ufw calls that turn the tagged rules into the wanted ones:
// deletes first, then adds, so a rule that changed action isn't skipped as
// existing. Locked rules are never deleted here.
func plan(want []contract.FirewallRule, tuples []tuple, v6 bool) ([][]string, error) {
	a, err := adds(want, tuples, v6)
	if err != nil {
		return nil, err
	}
	return append(deletes(want, tuples), a...), nil
}

// deletes are the tagged rules, locked ones aside, that want doesn't have.
func deletes(want []contract.FirewallRule, tuples []tuple) [][]string {
	var out [][]string
	wanted := allSpecs(want)
	for _, t := range tuples {
		if t.ID == "" || contract.LockedFirewallRule(t.ID) {
			continue
		}
		if !slices.ContainsFunc(wanted, func(s spec) bool { return s.ID == t.ID && s.matches(t) }) {
			out = appendOnce(out, deleteArgs(t))
		}
	}
	return out
}

// adds are the wanted rules UFW lacks, in each family it handles.
func adds(want []contract.FirewallRule, tuples []tuple, v6 bool) ([][]string, error) {
	var out [][]string
	for _, s := range allSpecs(want) {
		have := func(ipv6 bool) bool {
			return slices.ContainsFunc(tuples, func(t tuple) bool { return t.IPv6 == ipv6 && t.ID == s.ID && s.matches(t) })
		}
		var present bool
		switch {
		case s.Source.IsValid():
			present = have(s.Source.Addr().Is6())
		default:
			present = have(false) && (have(true) || !v6)
		}
		if present {
			continue
		}
		i := slices.IndexFunc(tuples, func(t tuple) bool { return t.ID == "" && s.samePlace(t) })
		switch {
		case i < 0:
			out = appendOnce(out, s.args())
		case tuples[i].Action == string(s.Action):
			// The installer's rule, or the owner's own: adding the same spec
			// with a comment tags it in place ("Rule updated", spike).
			out = appendOnce(out, slices.DeleteFunc(s.args(), func(a string) bool { return a == "prepend" }))
		default:
			return nil, &contract.AgentError{Code: contract.ErrConflict, Message: fmt.Sprintf(
				"Rule %s would replace a %s rule for %s port %s that Zimam didn't make. Remove that rule first.", s.ID, tuples[i].Action, s.Proto, s.port())}
		}
	}
	return out, nil
}

func allSpecs(rules []contract.FirewallRule) []spec {
	var out []spec
	for _, r := range rules {
		out = append(out, specs(r)...)
	}
	return out
}

func appendOnce(list [][]string, args []string) [][]string {
	if slices.ContainsFunc(list, func(a []string) bool { return slices.Equal(a, args) }) {
		return list
	}
	return append(list, args)
}

// Apply makes the owner's rules, plus the locked ones, the tagged rules, as a
// guarded change. Every call is dry-run first, so a rule UFW refuses changes
// nothing.
func Apply(ctx context.Context, d sysexec.Deps, rules []contract.FirewallRule) (contract.ChangeID, time.Time, error) {
	if err := CheckLockout(ctx, d, rules); err != nil {
		return "", time.Time{}, err
	}
	ssh, err := SSHPorts(ctx, d)
	if err != nil {
		return "", time.Time{}, err
	}
	tuples, err := readTuples(d.Paths)
	if err != nil {
		return "", time.Time{}, err
	}
	v6, err := ipv6Enabled(d.Paths.UFWDefaults())
	if err != nil {
		return "", time.Time{}, err
	}
	calls, err := plan(append(slices.Clone(rules), lockedRules(ssh, tuples)...), tuples, v6)
	if err != nil {
		return "", time.Time{}, err
	}
	for _, args := range calls {
		if err := runUFW(ctx, d, append([]string{"--dry-run"}, args...)...); err != nil {
			return "", time.Time{}, fmt.Errorf("ufw refused %s: %w", strings.Join(args, " "), err)
		}
	}
	return Guarded(ctx, d, func() error {
		for _, args := range calls {
			d.Logf("ufw %s", strings.Join(args, " "))
			if err := runUFW(ctx, d, args...); err != nil {
				return err
			}
		}
		return nil
	})
}

// AddSSHRules adds the locked rule of every SSH port that lacks one, before
// sshd moves to a new port (R06-D4).
func AddSSHRules(ctx context.Context, d sysexec.Deps, ports []contract.TCPPort) error {
	tuples, err := readTuples(d.Paths)
	if err != nil {
		return err
	}
	v6, err := ipv6Enabled(d.Paths.UFWDefaults())
	if err != nil {
		return err
	}
	calls, err := adds(lockedRules(ports, tuples)[:len(ports)], tuples, v6)
	if err != nil {
		return err
	}
	return runCalls(ctx, d, calls)
}

// RemoveStaleSSHRules deletes the locked rules of ports sshd no longer
// listens on, once the owner confirmed the SSH change (R06-D4).
func RemoveStaleSSHRules(ctx context.Context, d sysexec.Deps) error {
	ports, err := SSHPorts(ctx, d)
	if err != nil {
		return err
	}
	tuples, err := readTuples(d.Paths)
	if err != nil {
		return err
	}
	var calls [][]string
	for _, t := range tuples {
		port, ok := strings.CutPrefix(string(t.ID), "ssh-")
		if ok && !slices.ContainsFunc(ports, func(p contract.TCPPort) bool { return strconv.Itoa(int(p)) == port }) {
			calls = appendOnce(calls, deleteArgs(t))
		}
	}
	return runCalls(ctx, d, calls)
}

// runCalls dry-runs every call, then runs them.
func runCalls(ctx context.Context, d sysexec.Deps, calls [][]string) error {
	for _, args := range calls {
		if err := runUFW(ctx, d, append([]string{"--dry-run"}, args...)...); err != nil {
			return fmt.Errorf("ufw refused %s: %w", strings.Join(args, " "), err)
		}
	}
	for _, args := range calls {
		d.Logf("ufw %s", strings.Join(args, " "))
		if err := runUFW(ctx, d, args...); err != nil {
			return err
		}
	}
	return nil
}

// Guarded snapshots UFW's rules, starts a firewall change and applies it,
// reverting at once if applying fails. SSH port changes and Cloudflare's
// origin restriction use it too.
func Guarded(ctx context.Context, d sysexec.Deps, apply func() error) (contract.ChangeID, time.Time, error) {
	prev, err := Snapshot(d)
	if err != nil {
		return "", time.Time{}, err
	}
	id, deadline, err := changes.Begin(ctx, d, changes.Firewall, prev, Window)
	if err != nil {
		return "", time.Time{}, err
	}
	if err := apply(); err != nil {
		return "", time.Time{}, errors.Join(err, changes.Revert(ctx, d, id))
	}
	return id, deadline, nil
}

// rulesFile is one of UFW's rules files as it was.
type rulesFile struct {
	Content []byte      `json:"content"`
	Mode    fs.FileMode `json:"mode"`
	Missing bool        `json:"missing,omitempty"`
}

type rulesSnapshot struct {
	V4 rulesFile `json:"v4"`
	V6 rulesFile `json:"v6"`
}

// Snapshot copies user.rules and user6.rules: the whole files, so a revert
// is UFW's own state exactly (spike).
func Snapshot(d sysexec.Deps) ([]byte, error) {
	var s rulesSnapshot
	for _, f := range []struct {
		path string
		into *rulesFile
	}{{d.Paths.UFWUserRules(), &s.V4}, {d.Paths.UFWUser6Rules(), &s.V6}} {
		fi, err := os.Stat(f.path)
		if errors.Is(err, fs.ErrNotExist) {
			f.into.Missing = true
			continue
		}
		if err != nil {
			return nil, err
		}
		if f.into.Content, err = os.ReadFile(f.path); err != nil {
			return nil, err
		}
		f.into.Mode = fi.Mode().Perm()
	}
	return json.Marshal(s)
}

// Restore puts a snapshot's rules files back and reloads UFW, the revert of
// every firewall change.
func Restore(ctx context.Context, d sysexec.Deps, previous []byte) error {
	var s rulesSnapshot
	if err := json.Unmarshal(previous, &s); err != nil {
		return fmt.Errorf("firewall snapshot: %w", err)
	}
	var files []configtx.Change
	for _, f := range []struct {
		path string
		from rulesFile
	}{{d.Paths.UFWUserRules(), s.V4}, {d.Paths.UFWUser6Rules(), s.V6}} {
		if !f.from.Missing {
			files = append(files, configtx.RootFile(f.path, f.from.Content, f.from.Mode))
		}
	}
	return configtx.Apply(ctx, files, func(ctx context.Context) error {
		return configtx.RunTest(ctx, d.Run, sysexec.Cmd{Path: ufw, Args: []string{"reload"}})
	})
}
