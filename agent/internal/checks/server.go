package checks

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"slices"
	"sort"
	"strconv"
	"strings"
	"syscall"

	"zimam/agent/contract"
	"zimam/agent/internal/maintenance"
	"zimam/agent/internal/php"
)

const (
	ufw  = "/usr/sbin/ufw"
	sshd = "/usr/sbin/sshd"
)

// Server runs every server check of §9.7, in its order.
func Server(ctx context.Context, e Env) ([]contract.Finding, error) {
	out := []contract.Finding{firewallActive(ctx, e)}
	out = append(out, sshChecks(ctx, e)...)
	return append(out, securityUpdates(e), rebootRequired(e), stockPools(e), socketPermissions(e)), nil
}

// firewallActive reads ufw status and lists the allowed ports beside the
// TCP ports something listens on, from /proc/net/tcp and tcp6.
func firewallActive(ctx context.Context, e Env) contract.Finding {
	const check = "firewall_active"
	res, err := e.D.Run.Run(ctx, sysexecCmd(ufw, "status"))
	if err != nil {
		return finding(check, contract.FindingInconclusive, "ufw status failed: %v", err)
	}
	var allowed []string
	active := false
	for line := range strings.Lines(string(res.Stdout)) {
		f := strings.Fields(line)
		switch {
		case len(f) >= 2 && f[0] == "Status:":
			active = f[1] == "active"
		case len(f) >= 2 && f[1] == "ALLOW" && !slices.Contains(allowed, f[0]):
			allowed = append(allowed, f[0])
		case len(f) >= 3 && f[1] == "(v6)" && f[2] == "ALLOW" && !slices.Contains(allowed, f[0]):
			allowed = append(allowed, f[0])
		}
	}
	listening := listeningPorts(e)
	if !active {
		return finding(check, contract.FindingFail, "UFW isn't active. Listening TCP ports: %s.", listening)
	}
	return finding(check, contract.FindingPass, "UFW is active and allows %s. Listening TCP ports: %s.", orNone(strings.Join(allowed, ", ")), listening)
}

func orNone(s string) string {
	if s == "" {
		return "nothing"
	}
	return s
}

// listeningPorts are the local ports of TCP sockets in state LISTEN (0A).
func listeningPorts(e Env) string {
	seen := map[int]bool{}
	for _, path := range []string{e.D.Paths.ProcNetTCP(), e.D.Paths.ProcNetTCP6()} {
		b, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		for i, line := range strings.Split(string(b), "\n") {
			f := strings.Fields(line)
			if i == 0 || len(f) < 4 || f[3] != "0A" {
				continue
			}
			_, port, ok := strings.Cut(f[1], ":")
			if n, err := strconv.ParseUint(port, 16, 16); ok && err == nil {
				seen[int(n)] = true
			}
		}
	}
	var ports []int
	for p := range seen {
		ports = append(ports, p)
	}
	sort.Ints(ports)
	var s []string
	for _, p := range ports {
		s = append(s, strconv.Itoa(p))
	}
	return orNone(strings.Join(s, ", "))
}

// sshChecks read sshd -T, the effective configuration.
func sshChecks(ctx context.Context, e Env) []contract.Finding {
	res, err := e.D.Run.Run(ctx, sysexecCmd(sshd, "-T"))
	if err != nil {
		return []contract.Finding{
			finding("ssh_password_login", contract.FindingInconclusive, "sshd -T failed: %v", err),
			finding("ssh_root_login", contract.FindingInconclusive, "sshd -T failed: %v", err),
		}
	}
	conf := map[string]string{}
	for line := range strings.Lines(string(res.Stdout)) {
		if k, v, ok := strings.Cut(strings.TrimSpace(line), " "); ok {
			conf[k] = v
		}
	}
	password := finding("ssh_password_login", contract.FindingPass, "SSH refuses passwords.")
	if conf["passwordauthentication"] != "no" {
		password = finding("ssh_password_login", contract.FindingWarn, "SSH accepts passwords (passwordauthentication %s). Keys alone are safer.", orNone(conf["passwordauthentication"]))
	}
	root := finding("ssh_root_login", contract.FindingPass, "Root can't sign in over SSH with a password (permitrootlogin %s).", conf["permitrootlogin"])
	if v := conf["permitrootlogin"]; v != "no" && v != "prohibit-password" && v != "without-password" {
		root = finding("ssh_root_login", contract.FindingWarn, "permitrootlogin is %s; no or prohibit-password is safer.", orNone(v))
	}
	return []contract.Finding{password, root}
}

func securityUpdates(e Env) contract.Finding {
	const check = "security_updates_pending"
	u, err := maintenance.ReadUpdates(e.D.Paths)
	switch {
	case err != nil:
		return finding(check, contract.FindingInconclusive, "updates.json can't be read: %v", err)
	case u.Security == nil:
		return finding(check, contract.FindingInconclusive, "Pending updates haven't been counted yet.")
	case *u.Security > 0:
		return finding(check, contract.FindingWarn, "%d security updates are pending.", *u.Security)
	}
	return finding(check, contract.FindingPass, "No security updates are pending.")
}

func rebootRequired(e Env) contract.Finding {
	const check = "reboot_required"
	if _, err := os.Stat(e.D.Paths.RebootRequired()); err == nil {
		return finding(check, contract.FindingWarn, "Installed updates need a reboot.")
	}
	return finding(check, contract.FindingPass, "No reboot is needed.")
}

// stockPools looks for www.conf in every installed version's pool.d: it
// runs as www-data, which can read every site (§8.3).
func stockPools(e Env) contract.Finding {
	const check = "stock_www_pools_removed"
	var left []string
	for _, r := range contract.PHPAllowlist {
		installed, err := php.Installed(e.D, r.Version)
		if err != nil || !installed {
			continue
		}
		if _, err := os.Lstat(e.D.Paths.PHPWwwPool(r.Version)); !errors.Is(err, fs.ErrNotExist) {
			left = append(left, string(r.Version))
		}
	}
	if len(left) > 0 {
		return finding(check, contract.FindingFail, "The stock www pool is back for PHP %s.", strings.Join(left, ", "))
	}
	return finding(check, contract.FindingPass, "No installed PHP version has the stock www pool.")
}

// socketOwner is root, whom the socket must belong to; tests, which don't
// run as root, set their own.
var socketOwner = 0

// socketPermissions checks agent.sock is root:zimam-agent 0660 and that
// zimam-agent has no member but zimam (§5.1).
func socketPermissions(e Env) contract.Finding {
	const check = "agent_socket_permissions"
	gid, members, err := groupEntry(e.D.Paths.EtcGroup(), "zimam-agent")
	if err != nil {
		return finding(check, contract.FindingInconclusive, "%v", err)
	}
	var others []string
	for _, m := range members {
		if m != "zimam" {
			others = append(others, m)
		}
	}
	if len(others) > 0 {
		return finding(check, contract.FindingFail, "zimam-agent has members besides zimam: %s.", strings.Join(others, ", "))
	}
	fi, err := os.Lstat(e.D.Paths.Socket())
	if err != nil {
		return finding(check, contract.FindingFail, "The agent's socket can't be read: %v", err)
	}
	sys, ok := fi.Sys().(*syscall.Stat_t)
	switch {
	case fi.Mode().Type() != fs.ModeSocket:
		return finding(check, contract.FindingFail, "%s isn't a socket.", e.D.Paths.Socket())
	case fi.Mode().Perm() != 0o660:
		return finding(check, contract.FindingFail, "The socket's mode is %04o, not 0660.", fi.Mode().Perm())
	case !ok || int(sys.Uid) != socketOwner || int(sys.Gid) != gid:
		return finding(check, contract.FindingFail, "The socket isn't root:zimam-agent.")
	}
	return finding(check, contract.FindingPass, "The socket is root:zimam-agent 0660, and only zimam is in zimam-agent.")
}

// groupEntry reads a group's id and members from /etc/group.
func groupEntry(path, group string) (int, []string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, nil, err
	}
	for line := range strings.Lines(string(b)) {
		f := strings.Split(strings.TrimRight(line, "\n"), ":")
		if len(f) < 4 || f[0] != group {
			continue
		}
		gid, err := strconv.Atoi(f[2])
		if err != nil {
			return 0, nil, err
		}
		var members []string
		for _, m := range strings.Split(f[3], ",") {
			if m != "" {
				members = append(members, m)
			}
		}
		return gid, members, nil
	}
	return 0, nil, errors.New(path + " has no group " + group)
}
