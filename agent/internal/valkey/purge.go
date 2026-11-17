package valkey

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"zimam/agent/contract"
	"zimam/agent/internal/sysexec"
)

// purgeScript is one call of removing a site's keys, run as the admin. It
// scans up to ARGV[3] pages of 10000 keys from the cursor ARGV[1] that match
// the pattern ARGV[2], unlinks each page in batches (an UNLINK of too many keys
// would pass too many arguments), and answers "<next cursor> <keys unlinked>".
// Key names never leave the server, so a name with spaces, line breaks or
// quotes can't be split, merged or misread on the way through valkey-cli's
// output. A call examines at most pages x 10000 keys, which keeps it far under
// the server's busy-reply threshold; a scan of many keys is many calls.
const purgeScript = `local cursor = ARGV[1]
local removed = 0
for _ = 1, tonumber(ARGV[3]) do
  local r = redis.call('SCAN', cursor, 'MATCH', ARGV[2], 'COUNT', 10000)
  cursor = r[1]
  local keys = r[2]
  for i = 1, #keys, 500 do
    redis.call('UNLINK', unpack(keys, i, math.min(i + 499, #keys)))
  end
  removed = removed + #keys
  if cursor == '0' then break end
end
return cursor .. ' ' .. removed`

// purgePages is how many pages one call scans.
var purgePages = 5

// purgeBudget bounds one purge: no new call starts after it is spent, so a
// purge lasts at most this plus one call (cliTimeout). site.destroy has 300 s
// and needs 120 s of them for MySQL and its own steps, which leaves 180 s for
// everything Valkey does before them:
//
//	the admin PING, MySQL's check (its own 10 s bound) and the
//	server's run id                                               3 x 10 s
//	waiting for the lock on Valkey's users                        60 s
//	two purges, each purgeBudget plus its last call (10 s)        2 x (25 + 10) s
//	loading the users, and the restore if Valkey refuses them     2 x 10 s
//	saving the progress (local file writes)                       negligible
//
// which is 180 s. A purge that doesn't finish saves where it got to, so a
// smaller budget only means more runs.
var purgeBudget = 25 * time.Second

var (
	purgeReply = regexp.MustCompile(`^([0-9]+) ([0-9]+)$`)
	cursorRe   = regexp.MustCompile(`^[0-9]{1,20}$`)
)

// escapeGlob writes s so that a pattern matches it literally. Site names
// carry none of these characters; the escape is for the day they might.
func escapeGlob(s string) string {
	var b strings.Builder
	for _, r := range s {
		if strings.ContainsRune(`*?[]\`, r) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// sitePattern matches the keys of a site and no other site's: the colon after
// the name keeps s_shop: apart from s_shop-x:.
func sitePattern(site contract.SiteName) string {
	return "s_" + escapeGlob(string(site)) + ":*"
}

// PurgeArgs are the arguments of the valkey-cli call that removes one slice
// of a site's keys from a cursor. It is exported so that tests of callers can
// script the call.
func PurgeArgs(cursor string, site contract.SiteName) []string {
	return []string{"EVAL", purgeScript, "0", cursor, sitePattern(site), strconv.Itoa(purgePages)}
}

// purgeFrom removes the site's keys from a cursor until the scan ends (done)
// or purgeBudget is spent, and returns the cursor to continue from. The budget
// is checked between calls, so every run makes at least one call of progress.
// SCAN returns every key that is there from the first call to the last only
// within one server process: the hash seed is chosen at each start and the
// tables are rebuilt when the data is loaded, so a cursor from before a restart
// skips keys. A saved cursor is therefore used only by the process that gave it
// (see RemoveSiteUser).
func purgeFrom(ctx context.Context, d sysexec.Deps, site contract.SiteName, cursor string) (next string, removed int, done bool, err error) {
	if err := site.Validate(); err != nil {
		return cursor, 0, false, fmt.Errorf("site %q %v", site, err)
	}
	if !cursorRe.MatchString(cursor) {
		return cursor, 0, false, fmt.Errorf("%q is not a SCAN cursor", cursor)
	}
	end := time.Now().Add(purgeBudget)
	for {
		if err := ctx.Err(); err != nil {
			return cursor, removed, false, err
		}
		out, err := cliAdmin(ctx, d, PurgeArgs(cursor, site)...)
		if err != nil {
			return cursor, removed, false, err
		}
		m := purgeReply.FindStringSubmatch(out)
		if m == nil || !cursorRe.MatchString(m[1]) {
			return cursor, removed, false, fmt.Errorf("removing the keys of site %s was answered %q", site, out)
		}
		n, _ := strconv.Atoi(m[2])
		removed += n
		if cursor = m[1]; cursor == "0" {
			return cursor, removed, true, nil
		}
		if time.Now().After(end) {
			return cursor, removed, false, nil
		}
	}
}

// PurgeSiteKeys removes every key that starts with s_<name>: and returns how
// many it removed, as the admin. If purgeBudget runs out first it says so and
// to run the command again; the keys removed by then stay removed.
func PurgeSiteKeys(ctx context.Context, d sysexec.Deps, site contract.SiteName) (int, error) {
	_, n, done, err := purgeFrom(ctx, d, site, "0")
	if err != nil {
		return n, err
	}
	if !done {
		return n, fmt.Errorf("removing the keys of site %s took longer than %s (%d removed so far); run the command again", site, purgeBudget, n)
	}
	return n, nil
}

var runIDRe = regexp.MustCompile(`^[0-9a-f]{40}$`)

// runID is the id of the server process that answers, from INFO server. It
// changes at every start, and a SCAN cursor is good only within one process.
func runID(ctx context.Context, d sysexec.Deps) (string, error) {
	out, err := cliAdmin(ctx, d, "INFO", "server")
	if err != nil {
		return "", err
	}
	for line := range strings.Lines(out) {
		if id, ok := strings.CutPrefix(strings.TrimRight(line, "\r\n"), "run_id:"); ok {
			if !runIDRe.MatchString(id) {
				return "", fmt.Errorf("Valkey reported the run id %q", id)
			}
			return id, nil
		}
	}
	return "", errors.New("Valkey's INFO server has no run id")
}

// Progress is how far RemoveSiteUser got: Pass 0 is nothing yet, 1 is the
// purge before the user is removed, 2 the purge after; Cursor is where the
// purge in progress continues, and RunID the server process it belongs to.
type Progress struct {
	Pass   int
	Cursor string
	// RunID is the id of the server process that gave the cursor. A cursor is
	// good only for that process.
	RunID string
}

// Removal is what RemoveSiteUser needs of the caller's state. Load and Save
// run under the lock on Valkey's users; Save with the zero Progress forgets it.
// Clear ends the removal: it drops the site's hash and any progress.
type Removal struct {
	Load  func() (Progress, error)
	Save  func(Progress) error
	Clear func() error
}

// RemoveSiteUser removes a site's Valkey user and every key it left, in an
// order that leaves the site no way to write a key after the last purge, and
// all under one hold of the lock on Valkey's users:
//
//  1. purge the site's keys (pass 1);
//  2. load the users without the site (users() lists them all; the site is left
//     out here), which closes the user's connections;
//  3. purge again, for keys written between the first purge and the removal
//     (pass 2);
//  4. Clear, which the caller uses to drop the site's hash from its state.
//
// A purge has a time budget. When it is spent, or a call fails, the cursor it
// reached is saved with its pass and the error says to run the command again,
// which continues from there: a keyspace too large for one run is still
// removed after a few. The hash stays in the state until the last purge has
// finished, so a failure at any step leaves a state that makes the next run
// carry on, and every step can be repeated. Pass 2 is only saved once the user
// is removed, and a run that resumes it loads the users without the site again
// before it continues. A cursor is good only for the server process that
// gave it, so a run that finds a different run id (or none) in the saved progress,
// after a restart of Valkey, scans that pass again from 0; pass 2 stays pass 2,
// since the user is already removed. Until the hash is cleared the state is ahead of the live
// users: another change to Valkey's users renders the user back from the state,
// which is what a site that is not gone should have. Step 2 is applyLocked, the
// one writer of the users file, so a refused file is put back and loaded again.
func RemoveSiteUser(ctx context.Context, d sysexec.Deps, users func() ([]SiteUser, error), site contract.SiteName, rem Removal) error {
	if err := site.Validate(); err != nil {
		return fmt.Errorf("site %q %v", site, err)
	}
	unlock, err := lockUsers(ctx, d)
	if err != nil {
		return err
	}
	defer unlock()

	saved, err := rem.Load()
	if err != nil {
		return err
	}
	if saved.Pass < 0 || saved.Pass > 2 || (saved.Pass > 0 && !cursorRe.MatchString(saved.Cursor)) {
		return fmt.Errorf("the saved progress of removing site %s's keys is %+v", site, saved)
	}
	// Read before anything is scanned: a restart after this makes the run id
	// that is saved differ from the live one, which only costs a rescan.
	rid, err := runID(ctx, d)
	if err != nil {
		return err
	}

	// run is one pass: from the saved cursor, or from the start.
	run := func(pass int) error {
		cursor := "0"
		if saved.Pass == pass {
			if saved.RunID == rid {
				cursor = saved.Cursor
			} else {
				d.Logf("Valkey has restarted since the progress of removing site %s's keys was saved, and a cursor is good only for the process that gave it; scanning this pass again from the start", site)
			}
		}
		next, n, done, err := purgeFrom(ctx, d, site, cursor)
		if n > 0 {
			d.Logf("Removed %d key(s) of site %s from Valkey", n, site)
		}
		if done {
			return nil
		}
		if serr := rem.Save(Progress{Pass: pass, Cursor: next, RunID: rid}); serr != nil {
			return errors.Join(fmt.Errorf("removing the keys of site %s stopped and its progress could not be saved; run the command again", site), err, serr)
		}
		switch {
		case err == nil:
			return fmt.Errorf("removing the keys of site %s did not finish within %s (%d removed this time); the progress is saved, run the command again to continue", site, purgeBudget, n)
		case ctx.Err() != nil:
			return fmt.Errorf("removing the keys of site %s was interrupted; the progress is saved, run the command again to continue: %w", site, err)
		default:
			return fmt.Errorf("removing the keys of site %s failed; the progress is saved, run the command again to continue: %w", site, err)
		}
	}

	if saved.Pass < 2 {
		if err := run(1); err != nil {
			return err
		}
	}
	without := func() ([]SiteUser, error) {
		all, err := users()
		if err != nil {
			return nil, err
		}
		kept := make([]SiteUser, 0, len(all))
		for _, u := range all {
			if u.Name != site {
				kept = append(kept, u)
			}
		}
		return kept, nil
	}
	if err := applyLocked(ctx, d, without, nil); err != nil {
		return err
	}
	if saved.Pass != 2 {
		if err := rem.Save(Progress{Pass: 2, Cursor: "0", RunID: rid}); err != nil {
			return fmt.Errorf("the Valkey user of site %s is removed, but the progress could not be saved; run the command again: %w", site, err)
		}
	}
	if err := run(2); err != nil {
		return fmt.Errorf("the Valkey user of site %s is removed: %w", site, err)
	}
	if err := rem.Clear(); err != nil {
		return errors.Join(fmt.Errorf("the Valkey user and keys of site %s are removed, but its state could not be saved; run the command again", site), err)
	}
	return nil
}
