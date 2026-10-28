package helper

import (
	"fmt"
	"os/user"
	"slices"
	"strconv"
	"strings"

	"zimam/agent/contract"
)

// LookupAccount returns the account the helper may run as: zimam, or
// s_<site name>. It refuses root and every other account (D5), an account
// with uid, gid or a group 0, and one whose home isn't /home/<name>.
func LookupAccount(username string) (Account, error) {
	return lookupAccount(username, user.Lookup, (*user.User).GroupIds)
}

func lookupAccount(username string, lookup func(string) (*user.User, error), groupIDs func(*user.User) ([]string, error)) (Account, error) {
	site, isSite := strings.CutPrefix(username, "s_")
	if username != "zimam" && (!isSite || contract.SiteName(site).Validate() != nil) {
		return Account{}, fmt.Errorf("the helper runs only as zimam or s_<site>, not %q", username)
	}
	u, err := lookup(username)
	if err != nil {
		return Account{}, err
	}
	a := Account{Name: username, Home: u.HomeDir}
	if a.UID, err = parseID(u.Uid); err != nil {
		return Account{}, fmt.Errorf("account %s: uid %q: %w", username, u.Uid, err)
	}
	if a.GID, err = parseID(u.Gid); err != nil {
		return Account{}, fmt.Errorf("account %s: gid %q: %w", username, u.Gid, err)
	}
	gids, err := groupIDs(u)
	if err != nil {
		return Account{}, fmt.Errorf("account %s: groups: %w", username, err)
	}
	for _, g := range gids {
		id, err := parseID(g)
		if err != nil {
			return Account{}, fmt.Errorf("account %s: group %q: %w", username, g, err)
		}
		a.Groups = append(a.Groups, id)
	}
	if a.UID == 0 || a.GID == 0 || slices.Contains(a.Groups, 0) {
		return Account{}, fmt.Errorf("account %s has root's uid, gid or group", username)
	}
	if a.Home != "/home/"+username {
		return Account{}, fmt.Errorf("account %s has the home %q, not /home/%s", username, a.Home, username)
	}
	return a, nil
}

func parseID(s string) (uint32, error) {
	n, err := strconv.ParseUint(s, 10, 32)
	return uint32(n), err
}
