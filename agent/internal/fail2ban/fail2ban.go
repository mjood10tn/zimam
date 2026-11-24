// Package fail2ban runs fail2ban with Zimam's jails: SSH, the panel's
// sign-in and the sites' basic auth (R06-D5). Zimam writes only
// jail.d/zimam.local and filter.d/zimam-panel.conf.
package fail2ban

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"slices"
	"strings"
	"time"

	"zimam/agent/contract"
	"zimam/agent/internal/configtx"
	"zimam/agent/internal/php"
	"zimam/agent/internal/sysexec"
	"zimam/agent/internal/systemd"
	"zimam/agent/internal/templates"
)

const (
	client = "/usr/bin/fail2ban-client"
	regex  = "/usr/bin/fail2ban-regex"
)

// sampleLine is a failed sign-in as the journal backend hands it to the
// filter. The config test needs the filter to match it, not just compile.
const sampleLine = "zimam-panel-auth[1]: auth failure from 192.0.2.1 kind=password"

// Installed reports whether fail2ban's client is there.
func Installed(d sysexec.Deps) (bool, error) {
	_, err := os.Stat(d.Paths.Fail2banClient())
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}

// Configure installs fail2ban on first use and applies the jails. The test
// reloads fail2ban on the new files and checks it runs exactly the enabled
// jails; when it fails, configtx puts the old files back and the same test
// reloads those.
func Configure(ctx context.Context, d sysexec.Deps, p contract.Fail2banConfigParams) (contract.Fail2banStatusResult, error) {
	jails, err := renderJails(p)
	if err != nil {
		return contract.Fail2banStatusResult{}, err
	}
	filter, err := templates.Render("fail2ban/zimam-panel.conf", nil)
	if err != nil {
		return contract.Fail2banStatusResult{}, err
	}
	if ok, err := Installed(d); err != nil {
		return contract.Fail2banStatusResult{}, err
	} else if !ok {
		if err := php.InstallPackages(ctx, d, "fail2ban"); err != nil {
			return contract.Fail2banStatusResult{}, err
		}
	}
	if err := systemd.Systemctl(ctx, d, "enable", "--now", "fail2ban.service"); err != nil {
		return contract.Fail2banStatusResult{}, err
	}
	var want []string
	for _, j := range p.Jails {
		if j.Enabled {
			want = append(want, string(j.Name))
		}
	}
	first := true
	test := func(ctx context.Context) error {
		// fail2ban-client -t accepts a broken regex (spike), so the filter
		// must match a real line first.
		res, err := d.Run.Run(ctx, sysexec.Cmd{Path: regex, Args: []string{sampleLine, d.Paths.Fail2banPanelFilter()}})
		if err != nil {
			return &configtx.TestFailed{Output: err.Error()}
		}
		if !bytes.Contains(res.Stdout, []byte("1 matched")) {
			return &configtx.TestFailed{Output: "the zimam-panel filter doesn't match a failed sign-in:\n" + string(res.Stdout)}
		}
		for _, args := range [][]string{{"-t"}, {"reload"}} {
			if err := configtx.RunTest(ctx, d.Run, sysexec.Cmd{Path: client, Args: args}); err != nil {
				return err
			}
		}
		// Only the new files must run exactly these jails; the old ones,
		// put back after a failure, just have to load.
		if !first {
			return nil
		}
		first = false
		running, err := jailList(ctx, d)
		if err != nil {
			return &configtx.TestFailed{Output: err.Error()}
		}
		if slices.Sort(running); !slices.Equal(running, sorted(want)) {
			return &configtx.TestFailed{Output: fmt.Sprintf("fail2ban runs the jails %v, not %v", running, want)}
		}
		return nil
	}
	err = configtx.Apply(ctx, []configtx.Change{
		configtx.RootFile(d.Paths.Fail2banPanelFilter(), filter, 0o644),
		configtx.RootFile(d.Paths.Fail2banJails(), jails, 0o644),
	}, test)
	if err != nil {
		return contract.Fail2banStatusResult{}, err
	}
	return Status(ctx, d)
}

// renderJails renders zimam.local from the parsed ignore list, never the raw
// entries.
func renderJails(p contract.Fail2banConfigParams) ([]byte, error) {
	data := templates.Fail2banJails{Jails: p.Jails}
	for _, e := range p.Ignore {
		prefix, err := contract.ParseIPEntry(e)
		if err != nil {
			return nil, err
		}
		data.Ignore = append(data.Ignore, prefix)
	}
	return templates.Render("fail2ban/zimam.local", data)
}

func sorted(s []string) []string {
	s = slices.Clone(s)
	slices.Sort(s)
	return s
}

// jailList reads "Jail list:" from fail2ban-client status.
func jailList(ctx context.Context, d sysexec.Deps) ([]string, error) {
	res, err := d.Run.Run(ctx, sysexec.Cmd{Path: client, Args: []string{"status"}})
	if err != nil {
		return nil, err
	}
	for line := range strings.Lines(string(res.Stdout)) {
		_, list, ok := strings.Cut(line, "Jail list:")
		if !ok {
			continue
		}
		var out []string
		for name := range strings.SplitSeq(list, ",") {
			if name = strings.TrimSpace(name); name != "" {
				out = append(out, name)
			}
		}
		return out, nil
	}
	return nil, errors.New("fail2ban-client status gave no jail list")
}

// Status reports whether fail2ban is installed and running, and the
// addresses Zimam's jails ban, with when each ban ends.
func Status(ctx context.Context, d sysexec.Deps) (contract.Fail2banStatusResult, error) {
	res := contract.Fail2banStatusResult{Jails: []contract.Fail2banJailStatus{}}
	var err error
	if res.Installed, err = Installed(d); err != nil || !res.Installed {
		return res, err
	}
	res.Running = systemd.Systemctl(ctx, d, "is-active", "--quiet", "fail2ban.service") == nil
	if !res.Running {
		return res, nil
	}
	names, err := jailList(ctx, d)
	if err != nil {
		return res, err
	}
	for _, name := range names {
		if contract.JailName(name).Validate() != nil {
			continue // a jail the owner added; not Zimam's to show
		}
		out, err := d.Run.Run(ctx, sysexec.Cmd{Path: client, Args: []string{"get", name, "banip", "--with-time"}})
		if err != nil {
			return res, err
		}
		res.Jails = append(res.Jails, contract.Fail2banJailStatus{Name: contract.JailName(name), Banned: parseBans(out.Stdout)})
	}
	return res, nil
}

// parseBans reads "<address> \t<start> + <bantime> = <end>" lines; the times
// are the server's local time.
func parseBans(b []byte) []contract.Fail2banBan {
	bans := []contract.Fail2banBan{}
	s := bufio.NewScanner(bytes.NewReader(b))
	for s.Scan() {
		addr, rest, ok := strings.Cut(s.Text(), "\t")
		_, end, ok2 := strings.Cut(rest, "= ")
		if !ok || !ok2 {
			continue
		}
		until, err := time.ParseInLocation(time.DateTime, strings.TrimSpace(end), time.Local)
		addr = strings.TrimSpace(addr)
		if err != nil || contract.BanAddress(addr).Validate() != nil {
			continue
		}
		bans = append(bans, contract.Fail2banBan{Address: addr, Until: until.UTC()})
	}
	return bans
}

// Unban lifts a ban; fail2ban answers 1 when the address was banned there.
func Unban(ctx context.Context, d sysexec.Deps, jail contract.JailName, addr contract.BanAddress) (bool, error) {
	res, err := d.Run.Run(ctx, sysexec.Cmd{Path: client, Args: []string{"set", string(jail), "unbanip", string(addr)}})
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(string(res.Stdout)) == "1", nil
}
