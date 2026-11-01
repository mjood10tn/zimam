package maintenance

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strconv"
	"strings"
	"time"

	"zimam/agent/contract"
	"zimam/agent/internal/paths"
	"zimam/agent/internal/sysexec"
)

const (
	aptCheck = "/usr/lib/update-notifier/apt-check"
	aptGet   = "/usr/bin/apt-get"
)

// updatesMaxAge is how old the counts may get before a run counts again
// (§7.5: hourly).
const updatesMaxAge = time.Hour

// ReadUpdates reads updates.json, the pending update counts system.status
// reports. Before the first count there is none, and every value is null.
func ReadUpdates(p paths.Paths) (contract.UpdateCounts, error) {
	data, err := os.ReadFile(p.UpdatesCache())
	if errors.Is(err, fs.ErrNotExist) {
		return contract.UpdateCounts{}, nil
	}
	if err != nil {
		return contract.UpdateCounts{}, err
	}
	var u contract.UpdateCounts
	if err := contract.DecodeStrict(data, &u); err != nil {
		return contract.UpdateCounts{}, fmt.Errorf("%s: %w", p.UpdatesCache(), err)
	}
	return u, nil
}

// refreshUpdates counts the pending updates again once the stored counts
// are more than an hour old, missing, unreadable or from the future, and
// writes updates.json. A failed count leaves the last one, and its time, in
// place, so the next run tries again.
func refreshUpdates(ctx context.Context, d sysexec.Deps) error {
	now := d.Now().UTC()
	u, err := ReadUpdates(d.Paths)
	if err == nil && u.CheckedAt != nil && !u.CheckedAt.After(now) && now.Sub(*u.CheckedAt) <= updatesMaxAge {
		return nil
	}
	total, security, err := countUpdates(ctx, d)
	if err != nil {
		return fmt.Errorf("counting pending updates: %w", err)
	}
	d.Logf("%d updates pending, %d of them security updates", total, security)
	return writeRootJSON(d.Paths.UpdatesCache(), contract.UpdateCounts{Total: &total, Security: &security, CheckedAt: &now})
}

// countUpdates asks apt-check when update-notifier-common provides it, and
// otherwise counts a simulated dist-upgrade. Neither refreshes the package
// lists; apt's daily timer does that, so the counts are as fresh as its
// last run.
func countUpdates(ctx context.Context, d sysexec.Deps) (total, security int, err error) {
	_, err = os.Stat(d.Paths.AptCheck())
	if err == nil {
		res, err := d.Run.Run(ctx, sysexec.Cmd{Path: aptCheck})
		if err != nil {
			return 0, 0, err
		}
		return ParseAptCheck(res.Stderr)
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return 0, 0, err
	}
	res, err := d.Run.Run(ctx, sysexec.Cmd{Path: aptGet, Args: []string{"-s", "dist-upgrade"}})
	if err != nil {
		return 0, 0, err
	}
	total, security = ParseAptSimulate(res.Stdout)
	return total, security, nil
}

// ParseAptCheck reads apt-check's "<updates>;<security>", which it prints
// on stderr, with at most a trailing newline.
func ParseAptCheck(stderr []byte) (total, security int, err error) {
	a, b, ok := strings.Cut(strings.TrimSuffix(string(stderr), "\n"), ";")
	total, okTotal := count(a)
	security, okSecurity := count(b)
	if !ok || !okTotal || !okSecurity || security > total {
		return 0, 0, fmt.Errorf("apt-check printed %q, not <updates>;<security>", stderr)
	}
	return total, security, nil
}

func count(s string) (int, bool) {
	if s == "" || strings.Trim(s, "0123456789") != "" {
		return 0, false
	}
	n, err := strconv.Atoi(s)
	return n, err == nil
}

// ParseAptSimulate counts the Inst lines of apt-get -s dist-upgrade, and
// those whose candidate comes from a -security archive, as in
// "Inst gzip [1.14-1] (1.14-1.1 Ubuntu:26.04/resolute-updates,
// Ubuntu:26.04/resolute-security [amd64])".
func ParseAptSimulate(stdout []byte) (total, security int) {
	for line := range strings.Lines(string(stdout)) {
		if !strings.HasPrefix(line, "Inst ") {
			continue
		}
		total++
		if fromSecurity(line) {
			security++
		}
	}
	return total, security
}

// fromSecurity reads an Inst line's origins, "(<version> <origin>, <origin>
// [<arch>])", each ending in /<archive>, and reports whether one archive is
// a -security one. The package's name can't count: it sits before them.
func fromSecurity(line string) bool {
	open, end := strings.Index(line, " ("), strings.LastIndex(line, ")")
	if open < 0 || end < open {
		return false
	}
	_, origins, _ := strings.Cut(line[open+2:end], " ")
	if i := strings.LastIndex(origins, " ["); i >= 0 {
		origins = origins[:i]
	}
	for o := range strings.SplitSeq(origins, ", ") {
		if i := strings.LastIndex(o, "/"); i >= 0 && strings.HasSuffix(o[i+1:], "-security") {
			return true
		}
	}
	return false
}
