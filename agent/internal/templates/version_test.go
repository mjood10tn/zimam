package templates

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

// v1SiteRenders are the SHA-256 sums of what template version 1 rendered for
// static and PHP sites, taken before version 2's change (R02-D9). Hashes,
// not golden files, so a run of -update can't hide a change.
var v1SiteRenders = []struct {
	name, tmpl string
	data       any
	sum        string
}{
	{"static vhost http", "nginx/site.conf", SiteVhost{Name: "shop", Domain: "shop.example.com"}, "569b59687aa144fed66ca79e0e687f4970847a6da4b038a52c618e202969dd40"},
	{"static vhost https", "nginx/site.conf", SiteVhost{Name: "shop", Domain: "shop.example.com", HTTPS: true}, "02a98fb9b7d6e94d232acc53360d35dbedff14240af2761a718c3bb9e18f2e12"},
	{"static vhost https forced", "nginx/site.conf", SiteVhost{Name: "shop", Domain: "shop.example.com", HTTPS: true, ForceHTTPS: true}, "5257e8131107bdd5994f3ac1c6fccd62f9376b53266c8bbcb7862b49941b057d"},
	{"php vhost http", "nginx/site.conf", SiteVhost{Name: "shop", Domain: "shop.example.com", PHPVersion: "8.4"}, "0dbe23d48e49c550065354877b798ecb9ac3a61ba8477590fa6f891a564ee497"},
	{"php vhost https", "nginx/site.conf", SiteVhost{Name: "shop", Domain: "shop.example.com", PHPVersion: "8.4", HTTPS: true}, "3535e4f6a27b0c1bbcade98afc37f7ff39b566aeb853ef55a00442679bc7951b"},
	{"php vhost https forced", "nginx/site.conf", SiteVhost{Name: "shop", Domain: "shop.example.com", PHPVersion: "8.4", HTTPS: true, ForceHTTPS: true}, "1fd1ce703dd6c2afaf17bc448f74e47b5e74b7903eb88b33a9c4892c78a6558c"},
	{"security.conf defaults", "nginx/security.conf", SiteSecurity{Name: "shop", HeadersPreset: true}, "0600b49e59d1db5142266e5fc99c1cf968024e1ae315554a5521d1c9e25d6a69"},
	{"pool", "php/site-pool.conf", SitePool{Name: "shop", PHPVersion: "8.4"}, "cc98c1ffdec625171b124770d6bd70705fdb125221555c09338886f0d7266e04"},
	{"strict pool", "php/site-pool.conf", SitePool{Name: "shop", PHPVersion: "8.4", BlockRiskyFunctions: true, RestrictToSiteFolder: true}, "fdd81f42c66127c1a7bdea8aa5c5b7204dd14ccd404ca71ca1aef4fd2f5e3c88"},
	{"index.html", "site/index.html", SitePlaceholder{Name: "shop"}, "be855d882eaa475863241c86327c3cb9632eb8f8c6a1aa2fda8efb1ddbb7ddfc"},
	{"index.php", "site/index.php", SitePlaceholder{Name: "shop"}, "b528356ea0c0d3533fdd914e6288c6e27ca6d93cfcf86cde9da4934ec8249908"},
	{"site log rules", "logrotate/site.conf", SiteLogs{Name: "shop", RetentionDays: 14}, "481a42a8cf62598abe60e575fd5d6da7f648eec95c940baf5c025f0cca45fef8"},
}

// Version 2 changes only a laravel site's log rules, so a static or PHP site
// renders byte-identical files and keeps template_version 1 with nothing to
// re-render (R02-D9). Version 3 only adds files (R04-D7), so this still holds.
func TestV1SitesRenderUnchangedAtV3(t *testing.T) {
	if Version != 3 {
		t.Errorf("Version = %d, want 3", Version)
	}
	for _, c := range v1SiteRenders {
		out, err := Render(c.tmpl, c.data)
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		sum := sha256.Sum256(out)
		if got := hex.EncodeToString(sum[:]); got != c.sum {
			t.Errorf("%s: sha256 %s, want %s (version 1's):\n%s", c.name, got, c.sum, out)
		}
	}
}
