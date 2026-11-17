package commands

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"zimam/agent/contract"
	"zimam/agent/internal/jobs"
	"zimam/agent/internal/sites"
	"zimam/agent/internal/sysexec"
	"zimam/agent/internal/valkey"
)

func init() {
	jobPrechecks["cache.user"] = func(d sysexec.Deps) jobs.Precheck {
		return func(_ context.Context, p contract.Params) error {
			_, err := checkCacheUser(d, *p.(*contract.CacheUserParams))
			return err
		}
	}
	jobHandlers["cache.user"] = cacheUser
}

// checkCacheUser is cache.user's precheck, run again by the job: the site
// must exist and Valkey must be installed. It returns the site's state.
func checkCacheUser(d sysexec.Deps, p contract.CacheUserParams) (sites.State, error) {
	st, err := loadDBSite(d, p.Site)
	if err != nil {
		return sites.State{}, err
	}
	if err := requireValkey(d); err != nil {
		return sites.State{}, err
	}
	return st, nil
}

// requireValkey refuses a server where Zimam didn't install Valkey.
func requireValkey(d sysexec.Deps) error {
	ok, err := valkey.Installed(d)
	if err != nil {
		return err
	}
	if !ok {
		return conflict("Valkey isn't installed on this server. Install it first.")
	}
	return nil
}

// requireValkeyRunning is requireValkey and a PING as the admin, which the
// job makes before it changes anything.
func requireValkeyRunning(ctx context.Context, d sysexec.Deps) error {
	if err := requireValkey(d); err != nil {
		return err
	}
	if err := valkey.Ping(ctx, d); err != nil {
		return conflict("Valkey isn't answering: %v. Start it and try again.", err)
	}
	return nil
}

// cacheHashChange is the state change valkey.ApplyUsers runs under the lock
// on Valkey's users: it sets the site's cache user hash, or clears it when
// hash is empty, and records the hash it replaced in previous. Progress of removing the
// site's keys is dropped with it: a site given a user afresh starts afresh. The state is
// read again here, under the lock, so the change never overwrites a field
// another command saved since the job began. Its revert puts the old hash back.
func cacheHashChange(d sysexec.Deps, site contract.SiteName, hash string, previous *string) func() (func() error, error) {
	return func() (func() error, error) {
		st, err := loadDBSite(d, site)
		if err != nil {
			return nil, err
		}
		*previous = st.CacheUserHash
		oldHash, oldPurge := st.CacheUserHash, st.CachePurge
		st.CacheUserHash, st.CachePurge = hash, nil
		if err := sites.Save(d.Paths, st); err != nil {
			return nil, fmt.Errorf("the state of site %s could not be saved: %w", site, err)
		}
		return func() error {
			st, err := loadDBSite(d, site)
			if err != nil {
				return err
			}
			st.CacheUserHash, st.CachePurge = oldHash, oldPurge
			return sites.Save(d.Paths, st)
		}, nil
	}
}

// cacheUser creates the site's Valkey user, or sets a new password for the
// one it has. The user may reach only keys and channels that start with
// s_<name>: and may not run administrative or dangerous commands; the rule
// is in valkey's users file template.
//
// The password goes to nothing but SHA-256: the state file and Valkey's users
// file hold the hash, and nothing else of it is stored, logged, put in an
// argument, an error or a file.
//
// The steps, and what a failure leaves:
//
//  1. Valkey must be installed (recorded) and answer the admin's PING. If not,
//     the answer is a conflict and nothing has changed.
//  2. Under the lock on Valkey's users, the site's state gets the new hash,
//     the users file is rendered from every site's state, and Valkey loads it
//     (valkey.ApplyUsers). If the file can't be written or Valkey refuses it,
//     the previous file is put back and loaded and the previous hash is put
//     back in the state, so the site keeps the password it had; the error says
//     what was restored.
//  3. For a site that already had a user, every connection of that user is
//     closed, because Valkey keeps the connections of a user whose password
//     changed, so the old password would go on working for as long as one
//     is open. A first creation has none. The close runs whenever there was a
//     user before, even if the password is the same, so running the command
//     again after a failure here finishes the job. If the close fails the new
//     password is in force and the error says that old connections may remain.
func cacheUser(ctx context.Context, j *jobs.Context, p contract.Params) (any, error) {
	params := *p.(*contract.CacheUserParams)
	d := j.Deps()
	if _, err := checkCacheUser(d, params); err != nil {
		return nil, err
	}
	if err := requireValkeyRunning(ctx, d); err != nil {
		return nil, err
	}
	sum := sha256.Sum256([]byte(params.Password))
	var previous string
	if err := valkey.ApplyUsers(ctx, d, cacheUsers(d.Paths), cacheHashChange(d, params.Site, hex.EncodeToString(sum[:]), &previous)); err != nil {
		return nil, err
	}
	d.Logf("Set the password of site %s's Valkey user", params.Site)
	if previous != "" {
		n, err := valkey.KillUserConnections(ctx, d, params.Site)
		if err != nil {
			return nil, fmt.Errorf("the new password of site %s's Valkey user is in force, but its open connections could not be closed, so connections made with the old password may remain; run the command again: %w", params.Site, err)
		}
		d.Logf("Closed %d connection(s) of the site's Valkey user that were opened with the old password", n)
	}
	return contract.ServiceUserResult{Site: params.Site}, nil
}

// cachePurgeState is what site.destroy's removal of a site's Valkey user and
// keys keeps in the site's state: the progress of the purge, and, at the end,
// the removal of the hash. Each call reads the state again, under the lock on
// Valkey's users, so it never overwrites a field saved since.
func cachePurgeState(d sysexec.Deps, site contract.SiteName) valkey.Removal {
	update := func(change func(*sites.State)) error {
		st, err := loadDBSite(d, site)
		if err != nil {
			return err
		}
		change(&st)
		return sites.Save(d.Paths, st)
	}
	return valkey.Removal{
		Load: func() (valkey.Progress, error) {
			st, err := loadDBSite(d, site)
			if err != nil || st.CachePurge == nil {
				return valkey.Progress{}, err
			}
			return valkey.Progress{Pass: st.CachePurge.Pass, Cursor: st.CachePurge.Cursor, DB: st.CachePurge.DB, RunID: st.CachePurge.RunID}, nil
		},
		Save: func(p valkey.Progress) error {
			return update(func(st *sites.State) {
				st.CachePurge = nil
				if p.Pass != 0 {
					st.CachePurge = &sites.CachePurge{Pass: p.Pass, Cursor: p.Cursor, DB: p.DB, RunID: p.RunID}
				}
			})
		},
		Clear: func() error {
			return update(func(st *sites.State) { st.CacheUserHash, st.CachePurge = "", nil })
		},
	}
}
