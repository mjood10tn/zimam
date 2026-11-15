package sites

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strconv"
	"strings"

	"zimam/agent/internal/configtx"
	"zimam/agent/internal/paths"
	"zimam/agent/internal/sysexec"
)

// A site user's uid is never reused: a crontab, an at job or a file left
// behind by a deleted site would otherwise belong to the next site that got
// the number. The highest uid ever given is kept in uid-highwater.json.
type uidMark struct {
	Highest int `json:"highest"`
}

// readUIDMark is the recorded mark; 0 when none is written yet.
func readUIDMark(p paths.Paths) (int, error) {
	data, err := os.ReadFile(p.UIDHighwater())
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	var m uidMark
	if err := json.Unmarshal(data, &m); err != nil {
		return 0, fmt.Errorf("%s is not valid: %w", p.UIDHighwater(), err)
	}
	if m.Highest < 0 {
		return 0, fmt.Errorf("%s holds %d", p.UIDHighwater(), m.Highest)
	}
	return m.Highest, nil
}

// saveUIDMark records n as the highest uid given, root 0600.
func saveUIDMark(p paths.Paths, n int) error {
	data, err := json.MarshalIndent(uidMark{Highest: n}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(p.StateDir(), 0o700); err != nil {
		return err
	}
	noTest := func(context.Context) error { return nil }
	return configtx.Apply(context.Background(), []configtx.Change{
		configtx.RootFile(p.UIDHighwater(), append(data, '\n'), 0o600),
	}, noTest)
}

// uidRange is UID_MIN..UID_MAX from /etc/login.defs, 1000..60000 when unset.
func uidRange(p paths.Paths) (lo, hi int, err error) {
	lo, hi = 1000, 60000
	data, err := os.ReadFile(p.EtcLoginDefs())
	if errors.Is(err, fs.ErrNotExist) {
		return lo, hi, nil
	}
	if err != nil {
		return 0, 0, err
	}
	for line := range strings.Lines(string(data)) {
		f := strings.Fields(line)
		if len(f) < 2 || (f[0] != "UID_MIN" && f[0] != "UID_MAX") {
			continue
		}
		v, err := strconv.Atoi(f[1])
		if err != nil || v < 0 {
			return 0, 0, fmt.Errorf("%s has %s %q, which isn't a uid", p.EtcLoginDefs(), f[0], f[1])
		}
		if f[0] == "UID_MIN" {
			lo = v
		} else {
			hi = v
		}
	}
	if lo > hi {
		return 0, 0, fmt.Errorf("%s has UID_MIN %d above UID_MAX %d", p.EtcLoginDefs(), lo, hi)
	}
	return lo, hi, nil
}

// nextSiteUID is the uid for a new site user: one above the highest uid
// ever given (the mark) and the highest in passwd (the content of
// /etc/passwd) inside the user range, and never below its start. It refuses
// when that is past UID_MAX.
func nextSiteUID(p paths.Paths, passwd string) (int, error) {
	lo, hi, err := uidRange(p)
	if err != nil {
		return 0, err
	}
	highest, err := readUIDMark(p)
	if err != nil {
		return 0, err
	}
	highest = max(highest, lo-1)
	for line := range strings.Lines(passwd) {
		f := strings.Split(line, ":")
		if len(f) < 3 {
			continue
		}
		if uid, err := strconv.Atoi(f[2]); err == nil && uid >= lo && uid <= hi {
			highest = max(highest, uid)
		}
	}
	if highest+1 > hi {
		return 0, fmt.Errorf("no user id is left for a new site: the next one would pass UID_MAX (%d)", hi)
	}
	return highest + 1, nil
}

// groupGID is the gid of group in the /etc/group content at path, and
// whether the group is there.
func groupGID(path, group string) (int, bool, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	for line := range strings.Lines(string(data)) {
		f := strings.Split(strings.TrimSuffix(line, "\n"), ":")
		if len(f) >= 3 && f[0] == group {
			gid, err := strconv.Atoi(f[2])
			if err != nil {
				return 0, false, fmt.Errorf("group %s has gid %q", group, f[2])
			}
			return gid, true, nil
		}
	}
	return 0, false, nil
}

const groupadd = "/usr/sbin/groupadd"

// newSiteIDs makes the group for username with a fresh gid and returns it;
// the uid is the same number. The mark is saved before anything is created,
// so a crash leaves a skipped number, never a reused one. A group left by an
// attempt that stopped after groupadd is reused as it is.
func newSiteIDs(ctx context.Context, d sysexec.Deps, username string) (int, error) {
	if gid, ok, err := groupGID(d.Paths.EtcGroup(), username); err != nil {
		return 0, err
	} else if ok {
		if mark, err := readUIDMark(d.Paths); err != nil {
			return 0, err
		} else if mark < gid {
			if err := saveUIDMark(d.Paths, gid); err != nil {
				return 0, err
			}
		}
		return gid, nil
	}
	passwd, err := os.ReadFile(d.Paths.EtcPasswd())
	if err != nil {
		return 0, err
	}
	uid, err := nextSiteUID(d.Paths, string(passwd))
	if err != nil {
		return 0, err
	}
	if err := saveUIDMark(d.Paths, uid); err != nil {
		return 0, err
	}
	d.Logf("Creating the group %s (id %d)", username, uid)
	if _, err := d.Run.Run(ctx, sysexec.Cmd{Path: groupadd, Args: []string{"-g", strconv.Itoa(uid), username}}); err != nil {
		return 0, err
	}
	return uid, nil
}
