package sites

import (
	"errors"
	"io/fs"
	"reflect"
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
