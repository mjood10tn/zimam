package valkey

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"zimam/agent/contract"
	"zimam/agent/internal/templates"
)

// SiteUser is a site's Valkey user, s_<name>, and the SHA-256 of its
// password. Valkey's ACL file keeps only the hash, and so does the site's
// state file, which is where these come from.
type SiteUser struct {
	Name contract.SiteName
	Hash [sha256.Size]byte
}

var hashRe = regexp.MustCompile(`^[0-9a-f]{64}\z`)

// ParseHash reads a SHA-256 written as 64 lowercase hex digits, the form a
// site's state file holds.
func ParseHash(s string) ([sha256.Size]byte, error) {
	var h [sha256.Size]byte
	if !hashRe.MatchString(s) {
		return h, fmt.Errorf("%q is not a SHA-256 in 64 lowercase hex digits", s)
	}
	if _, err := hex.Decode(h[:], []byte(s)); err != nil {
		return h, err
	}
	return h, nil
}

// RenderACL is the ACL file: the default user off, the admin user, and one
// user for each site, sorted by name so that one set of users has one file.
// It is the only way the agent makes this file, so a re-run of valkey.install
// and a change to a site's user write the same one.
func RenderACL(admin [sha256.Size]byte, users []SiteUser) ([]byte, error) {
	sorted := slices.Clone(users)
	slices.SortFunc(sorted, func(a, b SiteUser) int { return strings.Compare(string(a.Name), string(b.Name)) })
	sites := make([]templates.ValkeySite, len(sorted))
	for i, u := range sorted {
		if err := u.Name.Validate(); err != nil {
			return nil, fmt.Errorf("site %q %v", u.Name, err)
		}
		if i > 0 && sorted[i-1].Name == u.Name {
			return nil, fmt.Errorf("site %s has two Valkey users", u.Name)
		}
		sites[i] = templates.ValkeySite{Name: u.Name, Hash: u.Hash}
	}
	return templates.Render("valkey/users.acl", templates.ValkeyACL{AdminHash: admin, Sites: sites})
}
