package valkey

import (
	"crypto/sha256"
	"strings"
	"testing"
)

func TestRenderACLRefusesWhatItCannotWriteSafely(t *testing.T) {
	h := sha256.Sum256([]byte("x"))
	admin := sha256.Sum256([]byte("admin"))
	for name, users := range map[string][]SiteUser{
		"an invalid site name": {{Name: "Shop", Hash: h}},
		"a reserved name":      {{Name: "panel", Hash: h}},
		"a name listed twice":  {{Name: "shop", Hash: h}, {Name: "shop", Hash: h}},
		"a name with a space":  {{Name: "my shop", Hash: h}},
	} {
		if out, err := RenderACL(admin, users); err == nil {
			t.Errorf("%s: RenderACL wrote\n%s", name, out)
		}
	}
	out, err := RenderACL(admin, []SiteUser{{Name: "zeta", Hash: h}, {Name: "alpha", Hash: h}, {Name: "mid-1", Hash: h}})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for l := range strings.Lines(string(out)) {
		if f := strings.Fields(l); len(f) > 1 && f[0] == "user" {
			names = append(names, f[1])
		}
	}
	if got, want := strings.Join(names, " "), "default zimam-admin s_alpha s_mid-1 s_zeta"; got != want {
		t.Errorf("users = %s, want %s", got, want)
	}
}

func TestParseHash(t *testing.T) {
	good := strings.Repeat("ab", 32)
	if h, err := ParseHash(good); err != nil || h[0] != 0xab || h[31] != 0xab {
		t.Errorf("ParseHash(good) = %x, %v", h, err)
	}
	for _, bad := range []string{"", good[:62], good + "a", strings.ToUpper(good), strings.Repeat("zz", 32), good[:63] + "\n"} {
		if _, err := ParseHash(bad); err == nil {
			t.Errorf("ParseHash accepted %q", bad)
		}
	}
}
