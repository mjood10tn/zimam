package sites

import (
	"errors"
	"io/fs"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"zimam/agent/contract"
	"zimam/agent/internal/paths"
)

func TestStateRoundTripAndAll(t *testing.T) {
	p := paths.Paths{Root: t.TempDir()}
	all, err := All(p)
	if err != nil || len(all) != 0 {
		t.Fatalf("All before any site = %v, %v; want none", all, err)
	}
	if _, err := Load(p, "shop"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("Load of a missing site = %v, want fs.ErrNotExist", err)
	}
	shop := State{Name: "shop", Domain: "shop.example.com", Type: contract.SitePHP, PHPVersion: "8.4", TemplateVersion: 1, Security: DefaultSecurity()}
	blog := State{Name: "blog", Domain: "blog.example.com", Type: contract.SiteStatic, TemplateVersion: 1, Security: DefaultSecurity()}
	for _, st := range []State{shop, blog} {
		if err := Save(p, st); err != nil {
			t.Fatal(err)
		}
	}
	got, err := Load(p, "shop")
	if err != nil || !reflect.DeepEqual(got, shop) {
		t.Errorf("Load = %+v, %v; want %+v", got, err, shop)
	}
	all, err = All(p)
	if err != nil || !reflect.DeepEqual(all, []State{blog, shop}) {
		t.Errorf("All = %+v, %v; want blog, then shop", all, err)
	}
}

// §9.6's defaults: the header preset on, everything else off or empty.
func TestDefaultSecurity(t *testing.T) {
	want := Security{HeadersPreset: true, IPAllow: contract.IPList{}, IPDeny: contract.IPList{}}
	if got := DefaultSecurity(); !reflect.DeepEqual(got, want) {
		t.Errorf("DefaultSecurity = %+v, want %+v", got, want)
	}
}

// The repository fields (v0.2.0) are optional, so a state file written
// before them still loads; set, they must be valid and come together.
func TestStateRepositoryFields(t *testing.T) {
	p := paths.Paths{Root: t.TempDir()}
	base := State{Name: "shop", Domain: "shop.example.com", Type: contract.SiteStatic, TemplateVersion: 1, Security: DefaultSecurity()}
	if err := Save(p, base); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(p.SiteState("shop"))
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"repository", "branch", "deploy_key_fingerprint"} {
		if strings.Contains(string(data), key) {
			t.Errorf("a site without a repository writes %q:\n%s", key, data)
		}
	}
	const fp = "SHA256:+DiY3wvvV6TuJJhbpZisF/zLDA0zPMSvHdkr4UvCOqU"
	ok := base
	ok.Repository, ok.Branch, ok.DeployKeyFingerprint = "octo/app", "main", fp
	if err := Save(p, ok); err != nil {
		t.Fatal(err)
	}
	if got, err := Load(p, "shop"); err != nil || !reflect.DeepEqual(got, ok) {
		t.Errorf("Load = %+v, %v; want %+v", got, err, ok)
	}
	for name, bad := range map[string]func(*State){
		"repository without branch": func(s *State) { s.Branch = "" },
		"branch without repository": func(s *State) { s.Repository = "" },
		"no fingerprint":            func(s *State) { s.DeployKeyFingerprint = "" },
		"bad repository":            func(s *State) { s.Repository = "octo/app.git" },
		"bad branch":                func(s *State) { s.Branch = "-rf" },
		"bad fingerprint":           func(s *State) { s.DeployKeyFingerprint = "MD5:aa:bb" },
		"short fingerprint":         func(s *State) { s.DeployKeyFingerprint = fp[:len(fp)-1] },
		"fingerprint alone": func(s *State) {
			s.Repository, s.Branch = "", ""
		},
	} {
		st := ok
		bad(&st)
		if err := Save(p, st); err == nil {
			t.Errorf("%s: saved %+v", name, st)
		}
	}
}

// The release fields (v0.2.0) are optional too. Set, keep_releases is 1-20,
// every release has a valid id, commit and time, the ids are unique, and at
// most one release is active.
func TestStateReleaseFields(t *testing.T) {
	p := paths.Paths{Root: t.TempDir()}
	created := time.Date(2026, 11, 15, 12, 0, 0, 0, time.UTC)
	const c1, c2 = "1111111aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "2222222bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	ok := State{Name: "shop", Domain: "shop.example.com", Type: contract.SiteStatic, TemplateVersion: 1, Security: DefaultSecurity(),
		KeepReleases: 3,
		Releases: []contract.Release{
			{ID: "20261115120000-1111111", Commit: c1, Created: created},
			{ID: "20261115120100-2222222", Commit: c2, Created: created.Add(time.Minute), Active: true},
		},
	}
	if err := Save(p, ok); err != nil {
		t.Fatal(err)
	}
	if got, err := Load(p, "shop"); err != nil || !reflect.DeepEqual(got, ok) {
		t.Errorf("Load = %+v, %v; want %+v", got, err, ok)
	}
	for name, bad := range map[string]func(*State){
		"keep_releases 21": func(s *State) { s.KeepReleases = 21 },
		"keep_releases -1": func(s *State) { s.KeepReleases = -1 },
		"bad id":           func(s *State) { s.Releases[0].ID = "initial" },
		"bad commit":       func(s *State) { s.Releases[0].Commit = "abc" },
		"no time":          func(s *State) { s.Releases[0].Created = time.Time{} },
		"same id twice":    func(s *State) { s.Releases[1].ID = s.Releases[0].ID },
		"two active":       func(s *State) { s.Releases[0].Active = true },
	} {
		st := ok
		st.Releases = slices.Clone(ok.Releases)
		bad(&st)
		if err := Save(p, st); err == nil {
			t.Errorf("%s: saved %+v", name, st)
		}
	}
}

// A state file written before databases existed loads and saves again
// without one changed byte.
func TestStateWithoutDatabasesKeepsItsBytes(t *testing.T) {
	const v020 = `{
  "name": "shop",
  "domain": "shop.example.com",
  "type": "php",
  "php_version": "8.4",
  "template_version": 3,
  "security": {
    "force_https": true,
    "hsts_max_age": 0,
    "hsts_include_subdomains": false,
    "headers_preset": true,
    "block_risky_functions": false,
    "restrict_to_site_folder": false,
    "ip_allow": [],
    "ip_deny": [],
    "password_protection": false
  },
  "repository": "octo/app",
  "branch": "main",
  "deploy_key_fingerprint": "SHA256:abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOP0",
  "keep_releases": 3
}
`
	p := paths.Paths{Root: t.TempDir()}
	if err := os.MkdirAll(p.SitesStateDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.SiteState("shop"), []byte(v020), 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := Load(p, "shop")
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Databases) != 0 || st.DBUser {
		t.Errorf("databases %v, db_user %v on a file without them", st.Databases, st.DBUser)
	}
	if err := Save(p, st); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(p.SiteState("shop"))
	if err != nil || string(got) != v020 {
		t.Errorf("saved file differs (%v):\n%s", err, got)
	}
}

func TestStateDatabases(t *testing.T) {
	p := paths.Paths{Root: t.TempDir()}
	base := State{Name: "my-shop", Domain: "shop.example.com", Type: contract.SiteStatic, TemplateVersion: 1, Security: DefaultSecurity()}

	withDBs := base
	withDBs.Databases = []contract.DatabaseName{"s_my_shop_a", "s_my_shop_b"}
	withDBs.DBUser = true
	if err := Save(p, withDBs); err != nil {
		t.Fatal(err)
	}
	got, err := Load(p, "my-shop")
	if err != nil || !reflect.DeepEqual(got, withDBs) {
		t.Fatalf("Load = %+v, %v; want %+v", got, err, withDBs)
	}
	data, _ := os.ReadFile(p.SiteState("my-shop"))
	if !strings.Contains(string(data), `"databases": [`) || !strings.Contains(string(data), `"db_user": true`) {
		t.Errorf("state file lacks the fields:\n%s", data)
	}

	for name, dbs := range map[string][]contract.DatabaseName{
		"another site's prefix": {"s_other_a"},
		"no suffix":             {"s_my_shop_"},
		"not sorted":            {"s_my_shop_b", "s_my_shop_a"},
		"listed twice":          {"s_my_shop_a", "s_my_shop_a"},
		"reserved":              {"mysql"},
		"a backtick":            {"s_my_shop_a`b"},
	} {
		bad := base
		bad.Databases = dbs
		if err := Save(p, bad); err == nil {
			t.Errorf("%s: Save accepted %v", name, dbs)
		}
	}
}

// A state file written before cache users existed loads and saves again
// without one changed byte, and a site with a cache user keeps its hash.
func TestStateCacheUserHash(t *testing.T) {
	p := paths.Paths{Root: t.TempDir()}
	base := State{Name: "my-shop", Domain: "shop.example.com", Type: contract.SiteStatic, TemplateVersion: 1, Security: DefaultSecurity()}
	if err := Save(p, base); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(p.SiteState("my-shop"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(before), "cache_user_hash") {
		t.Fatalf("a site without a cache user writes the field:\n%s", before)
	}
	st, err := Load(p, "my-shop")
	if err != nil || st.CacheUserHash != "" {
		t.Fatalf("Load = %+v, %v", st, err)
	}
	if err := Save(p, st); err != nil {
		t.Fatal(err)
	}
	if after, _ := os.ReadFile(p.SiteState("my-shop")); string(after) != string(before) {
		t.Errorf("saving a state without a cache user changed its bytes:\n%s\nwas\n%s", after, before)
	}

	withHash := base
	withHash.CacheUserHash = strings.Repeat("ab", 32)
	if err := Save(p, withHash); err != nil {
		t.Fatal(err)
	}
	got, err := Load(p, "my-shop")
	if err != nil || got.CacheUserHash != withHash.CacheUserHash {
		t.Fatalf("Load = %+v, %v; want the hash back", got, err)
	}
	if data, _ := os.ReadFile(p.SiteState("my-shop")); !strings.Contains(string(data), `"cache_user_hash": "`+withHash.CacheUserHash+`"`) {
		t.Errorf("state file lacks the field:\n%s", data)
	}

	for name, hash := range map[string]string{
		"too short":  strings.Repeat("ab", 31),
		"too long":   strings.Repeat("ab", 33),
		"upper case": strings.Repeat("AB", 32),
		"not hex":    strings.Repeat("xy", 32),
		"a newline":  strings.Repeat("ab", 31) + "a\n",
	} {
		bad := base
		bad.CacheUserHash = hash
		if err := Save(p, bad); err == nil {
			t.Errorf("%s: Save accepted %q", name, hash)
		}
	}
}

// The saved progress of removing a site's Valkey keys is left out of a state
// that has none, round-trips when set, and is refused when malformed.
func TestStateCachePurge(t *testing.T) {
	p := paths.Paths{Root: t.TempDir()}
	base := State{Name: "my-shop", Domain: "shop.example.com", Type: contract.SiteStatic, TemplateVersion: 1, Security: DefaultSecurity(), CacheUserHash: strings.Repeat("ab", 32)}
	if err := Save(p, base); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(p.SiteState("my-shop"))
	if strings.Contains(string(before), "cache_purge") {
		t.Fatalf("a state without progress writes the field:\n%s", before)
	}
	st, err := Load(p, "my-shop")
	if err != nil {
		t.Fatal(err)
	}
	if err := Save(p, st); err != nil {
		t.Fatal(err)
	}
	if after, _ := os.ReadFile(p.SiteState("my-shop")); string(after) != string(before) {
		t.Errorf("the bytes changed:\n%s\nwas\n%s", after, before)
	}
	withPurge := base
	withPurge.CachePurge = &CachePurge{Pass: 2, Cursor: "18446744073709551615", DB: 15, RunID: strings.Repeat("0f", 20)}
	if err := Save(p, withPurge); err != nil {
		t.Fatal(err)
	}
	got, err := Load(p, "my-shop")
	if err != nil || got.CachePurge == nil || *got.CachePurge != *withPurge.CachePurge {
		t.Fatalf("Load = %+v, %v", got, err)
	}
	for name, c := range map[string]*CachePurge{
		"pass 0": {Pass: 0, Cursor: "1"}, "pass 3": {Pass: 3, Cursor: "1"},
		"no cursor": {Pass: 1}, "letters": {Pass: 1, Cursor: "1a"}, "negative": {Pass: 1, Cursor: "-1"},
		"too long": {Pass: 1, Cursor: strings.Repeat("9", 21)}, "a newline": {Pass: 1, Cursor: "1\n"},
		"a negative database": {Pass: 1, Cursor: "1", DB: -1}, "a database too far": {Pass: 1, Cursor: "1", DB: 65536},
		"a short run id": {Pass: 1, Cursor: "1", RunID: "abc"}, "an upper-case run id": {Pass: 1, Cursor: "1", RunID: strings.Repeat("AB", 20)},
	} {
		bad := base
		bad.CachePurge = c
		if err := Save(p, bad); err == nil {
			t.Errorf("%s: Save accepted %+v", name, c)
		}
	}
	// Database 0 is left out of the file, and a file without the field reads as 0.
	zero := base
	zero.CachePurge = &CachePurge{Pass: 1, Cursor: "5"}
	if err := Save(p, zero); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(p.SiteState("my-shop")); strings.Contains(string(data), `"db"`) {
		t.Errorf("database 0 is written:\n%s", data)
	}
	if got, err := Load(p, "my-shop"); err != nil || got.CachePurge == nil || got.CachePurge.DB != 0 {
		t.Errorf("Load = %+v, %v; want database 0", got.CachePurge, err)
	}
	noHash := withPurge
	noHash.CacheUserHash = ""
	if err := Save(p, noHash); err == nil {
		t.Error("Save accepted progress for a site with no cache user")
	}
}
