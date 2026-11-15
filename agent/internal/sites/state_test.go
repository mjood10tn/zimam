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
