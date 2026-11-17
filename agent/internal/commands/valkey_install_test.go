package commands

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"strings"
	"testing"

	"zimam/agent/contract"
	"zimam/agent/internal/paths"
	"zimam/agent/internal/sites"
)

func saveSite(t *testing.T, p paths.Paths, name contract.SiteName, hash string) {
	t.Helper()
	st := sites.State{Name: name, Domain: contract.Domain(string(name) + ".example.com"), Type: contract.SiteStatic, TemplateVersion: 1, Security: sites.DefaultSecurity(), CacheUserHash: hash}
	if err := sites.Save(p, st); err != nil {
		t.Fatal(err)
	}
}

// valkey.install renders the ACL file from the sites that have a cache user,
// so running it again keeps them.
func TestCacheUsersAreTheSitesWithAHash(t *testing.T) {
	p := paths.Paths{Root: t.TempDir()}
	sum := sha256.Sum256([]byte("pw"))
	saveSite(t, p, "shop", hex.EncodeToString(sum[:]))
	saveSite(t, p, "blog", "")
	saveSite(t, p, "app", hex.EncodeToString(sum[:]))
	users, err := cacheUsers(p)()
	if err != nil {
		t.Fatal(err)
	}
	if len(users) != 2 || users[0].Name != "app" || users[1].Name != "shop" || users[0].Hash != sum {
		t.Errorf("cacheUsers = %+v, want app and shop with the hash", users)
	}
}

func TestCacheUsersWithoutSites(t *testing.T) {
	users, err := cacheUsers(paths.Paths{Root: t.TempDir()})()
	if err != nil || len(users) != 0 {
		t.Errorf("cacheUsers = %+v, %v; want none", users, err)
	}
}

// A state file that can't be read must stop the install: writing the ACL
// file without that site's user would lock it out.
func TestCacheUsersRefusesAnUnreadableSiteState(t *testing.T) {
	p := paths.Paths{Root: t.TempDir()}
	saveSite(t, p, "shop", "")
	if err := os.WriteFile(p.SiteState("shop"), []byte(`{"name":`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := cacheUsers(p)(); err == nil || !strings.Contains(err.Error(), "shop") {
		t.Errorf("cacheUsers = %v, want an error naming the site", err)
	}
}

// The users loader is the one valkey.install and every apply use: a site
// whose destroy has begun removing its keys gets no user from it.
func TestCacheUsersSkipASiteBeingDestroyed(t *testing.T) {
	p := paths.Paths{Root: t.TempDir()}
	sum := sha256.Sum256([]byte("pw"))
	saveSite(t, p, "shop", hex.EncodeToString(sum[:]))
	saveSite(t, p, "blog", hex.EncodeToString(sum[:]))
	st, err := sites.Load(p, "shop")
	if err != nil {
		t.Fatal(err)
	}
	st.CachePurge = &sites.CachePurge{Pass: 2, Cursor: "5"}
	if err := sites.Save(p, st); err != nil {
		t.Fatal(err)
	}
	users, err := cacheUsers(p)()
	if err != nil || len(users) != 1 || users[0].Name != "blog" {
		t.Errorf("cacheUsers = %+v, %v; want only blog", users, err)
	}
}
