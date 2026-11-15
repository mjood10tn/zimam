package sites

import (
	"errors"
	"io/fs"
	"os"
	"reflect"
	"strings"
	"testing"

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
