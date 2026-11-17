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

// PurgeScript is one step of removing a site's keys, run as the admin: it
// scans a page of keys that match the pattern, unlinks them in batches, and
// answers "<next cursor> <keys unlinked>". The key names never leave the
// server, so a name with spaces, line breaks or quotes can't be split, merged
// or misread on the way through valkey-cli's output. The batches keep each
// UNLINK within the number of arguments a script may pass. It is exported so
// that tests of callers can script the call.
const PurgeScript = `local r = redis.call('SCAN', ARGV[1], 'MATCH', ARGV[2], 'COUNT', 1000)
local keys = r[2]
for i = 1, #keys, 500 do
  redis.call('UNLINK', unpack(keys, i, math.min(i + 499, #keys)))
end
return r[1] .. ' ' .. #keys`

// purgeBudget bounds one purge, so two of them and the rest of a destroy fit
// the job's time. A purge that can't finish says so and is run again.
var purgeBudget = time.Minute

var purgeReply = regexp.MustCompile(`^([0-9]+) ([0-9]+)$`)

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

// PurgeSiteKeys removes every key that starts with s_<name>: and returns how
// many it removed. It scans the whole keyspace in pages and unlinks each
// page, as the admin, and gives up after purgeBudget with an error that says
// to run the command again; the keys removed by then stay removed.
func PurgeSiteKeys(ctx context.Context, d sysexec.Deps, site contract.SiteName) (int, error) {
	if err := site.Validate(); err != nil {
		return 0, fmt.Errorf("site %q %v", site, err)
	}
	pattern := sitePattern(site)
	bctx, cancel := context.WithTimeout(ctx, purgeBudget)
	defer cancel()
	total, cursor := 0, "0"
	for {
		if err := bctx.Err(); err != nil {
			return total, purgeStopped(ctx, site, total, err)
		}
		out, err := cliAdmin(bctx, d, "EVAL", PurgeScript, "0", cursor, pattern)
		if err != nil {
			if bctx.Err() != nil {
				return total, purgeStopped(ctx, site, total, bctx.Err())
			}
			return total, err
		}
		m := purgeReply.FindStringSubmatch(out)
		if m == nil {
			return total, fmt.Errorf("removing the keys of site %s was answered %q", site, out)
		}
		n, _ := strconv.Atoi(m[2])
		total += n
		if cursor = m[1]; cursor == "0" {
			return total, nil
		}
	}
}

func purgeStopped(ctx context.Context, site contract.SiteName, total int, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return fmt.Errorf("removing the keys of site %s took longer than %s (%d removed so far, and they stay removed); run the command again: %w", site, purgeBudget, total, err)
}

// RemoveSiteUser removes a site's Valkey user and every key it left, in an
// order that leaves the site no way to write a key after the last purge, and
// all under one hold of the lock on Valkey's users:
//
//  1. purge the site's keys;
//  2. load the users without the site (users() lists them all; the site is left
//     out here), which closes the user's connections;
//  3. purge again, for keys written between the first purge and the removal;
//  4. clear, which the caller uses to drop the site's hash from its state.
//
// The hash stays in the state until the last purge has succeeded, so a failure
// at any step leaves a state that makes the next run do all four again, and
// each step can be repeated. Until then the state is ahead of the live users:
// another change to Valkey's users renders the user back from the state, which
// is what a site that is not gone should have. Step 2 is applyLocked, the one
// writer of the users file, so a refused file is put back and loaded again.
func RemoveSiteUser(ctx context.Context, d sysexec.Deps, users func() ([]SiteUser, error), site contract.SiteName, clear func() error) error {
	if err := site.Validate(); err != nil {
		return fmt.Errorf("site %q %v", site, err)
	}
	unlock, err := lockUsers(ctx, d)
	if err != nil {
		return err
	}
	defer unlock()

	if n, err := PurgeSiteKeys(ctx, d, site); err != nil {
		return err
	} else {
		d.Logf("Removed %d key(s) of site %s from Valkey", n, site)
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
	if n, err := PurgeSiteKeys(ctx, d, site); err != nil {
		return fmt.Errorf("the Valkey user of site %s is removed, but its last keys could not be: %w", site, err)
	} else if n > 0 {
		d.Logf("Removed %d key(s) the site wrote before its user was removed", n)
	}
	if err := clear(); err != nil {
		return errors.Join(fmt.Errorf("the Valkey user and keys of site %s are removed, but its state could not be saved; run the command again", site), err)
	}
	return nil
}
