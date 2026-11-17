package contract

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestDatabaseNameRules(t *testing.T) {
	checkValues(t,
		[]DatabaseName{"a", "s_shop_main", "s_my_shop_db2", DatabaseName("a" + strings.Repeat("b", 63))},
		[]DatabaseName{"a`b", "a;b", "A", "1a", "_a", "", "a-b", "a b", "a.b", "a\n", "ش",
			"mysql", "sys", "information_schema", "performance_schema", "zimam",
			DatabaseName("a" + strings.Repeat("b", 64))},
	)
}

func TestServicePasswordRules(t *testing.T) {
	ok := strings.Repeat("aB3_-", 5) // 25 characters
	checkValues(t,
		[]ServicePassword{ServicePassword(strings.Repeat("a", 24)), ServicePassword(ok), ServicePassword(strings.Repeat("A", 64))},
		[]ServicePassword{
			ServicePassword(strings.Repeat("a", 23)), ServicePassword(strings.Repeat("a", 65)),
			ServicePassword(strings.Repeat("a", 23) + "'"), ServicePassword(strings.Repeat("a", 23) + `\`),
			ServicePassword(strings.Repeat("a", 23) + " "), ServicePassword(strings.Repeat("a", 24) + "\n"),
			ServicePassword(strings.Repeat("a", 23) + "#"), "",
		},
	)
}

func TestDatabasePrefix(t *testing.T) {
	for site, want := range map[SiteName]string{"shop": "s_shop_", "my-shop": "s_my_shop_", "a-b-c": "s_a_b_c_"} {
		if got := DatabasePrefix(site); got != want {
			t.Errorf("DatabasePrefix(%q) = %q, want %q", site, got, want)
		}
	}
}

func TestDatabaseBelongsTo(t *testing.T) {
	for _, c := range []struct {
		db   DatabaseName
		site SiteName
		want bool
	}{
		{"s_shop_main", "shop", true},
		{"s_my_shop_db", "my-shop", true},
		{"s_shop_", "shop", false}, // nothing after the prefix
		{"s_shop", "shop", false},
		{"s_shopx_main", "shop", false},
		{"s_shop_main", "my-shop", false},
		{"main", "shop", false},
	} {
		if got := c.db.BelongsTo(c.site); got != c.want {
			t.Errorf("%q.BelongsTo(%q) = %v, want %v", c.db, c.site, got, c.want)
		}
	}
}

func TestDbCreateAndDropParams(t *testing.T) {
	for _, p := range []Params{
		&DbCreateParams{Site: "shop", Database: "s_shop_main"},
		&DbDropParams{Site: "shop", Database: "s_shop_main"},
	} {
		if f := fields(t, p); f != nil {
			t.Errorf("%#v: %v", p, f)
		}
	}
	wantRefused(t, &DbCreateParams{Site: "Shop", Database: "mysql"}, "site", "database")
	wantRefused(t, &DbDropParams{Site: "Shop", Database: "mysql"}, "site", "database")
	// A valid name that is another site's, or has nothing after the prefix.
	for _, db := range []DatabaseName{"s_other_main", "s_shop_", "main"} {
		f := fields(t, &DbCreateParams{Site: "shop", Database: db})
		if !strings.Contains(f["database"], "s_shop_") {
			t.Errorf("db.create %q: fields = %v, want database refused for its prefix", db, f)
		}
		wantRefused(t, &DbDropParams{Site: "shop", Database: db}, "database")
	}
}

func TestDbListParams(t *testing.T) {
	if f := fields(t, &DbListParams{}); f != nil {
		t.Errorf("no site: %v", f)
	}
	if f := fields(t, &DbListParams{Site: "shop"}); f != nil {
		t.Errorf("a site: %v", f)
	}
	wantRefused(t, &DbListParams{Site: "Shop"}, "site")
	if b, _ := json.Marshal(DbListParams{}); string(b) != "{}" {
		t.Errorf("empty params encode as %s, want {}", b)
	}
}

func TestDbListResultEncodesEmptyList(t *testing.T) {
	b, err := json.Marshal(NewDbListResult())
	if err != nil || string(b) != `{"databases":[]}` {
		t.Errorf("empty result = %s, %v; want an empty array, not null", b, err)
	}
}

func TestPasswordParams(t *testing.T) {
	pw := ServicePassword(strings.Repeat("a", 32))
	for _, p := range []Params{&DbUserParams{Site: "shop", Password: pw}, &CacheUserParams{Site: "shop", Password: pw}} {
		if f := fields(t, p); f != nil {
			t.Errorf("%#v: %v", p, f)
		}
	}
	wantRefused(t, &DbUserParams{Site: "Shop", Password: "short"}, "site", "password")
	wantRefused(t, &CacheUserParams{Site: "Shop", Password: "short"}, "site", "password")
}

// Passwords are secret in every command that carries one, so the audit log
// and agent_actions redact them and the schemas mark them.
func TestPasswordFieldsAreSecret(t *testing.T) {
	for _, name := range []string{"db.user", "cache.user"} {
		d, ok := Lookup(name)
		if !ok {
			t.Fatalf("%s isn't registered", name)
		}
		if !d.Audited {
			t.Errorf("%s isn't audited", name)
		}
		if !secretField(t, reflect.TypeOf(d.Params()).Elem(), "password") {
			t.Errorf("%s's password isn't secret", name)
		}
	}
	d, _ := Lookup("db.create")
	if secretField(t, reflect.TypeOf(d.Params()).Elem(), "database") {
		t.Error("db.create's database is marked secret")
	}
}

func TestDatabaseCommandsSubjects(t *testing.T) {
	for name, p := range map[string]Params{
		"db.create":      &DbCreateParams{Site: "shop", Database: "s_shop_main"},
		"db.drop":        &DbDropParams{Site: "shop", Database: "s_shop_main"},
		"db.user":        &DbUserParams{Site: "shop"},
		"cache.user":     &CacheUserParams{Site: "shop"},
		"mysql.install":  &MySQLInstallParams{},
		"valkey.install": &ValkeyInstallParams{},
	} {
		d, ok := Lookup(name)
		if !ok || d.Kind != KindJob || !d.Audited {
			t.Fatalf("%s = %+v, %v; want an audited job", name, d, ok)
		}
		want := "site:shop"
		if strings.HasSuffix(name, ".install") {
			want = "server"
		}
		if got := d.Subject(p); got != want {
			t.Errorf("%s subject %q, want %q", name, got, want)
		}
	}
	d, ok := Lookup("db.list")
	if !ok || d.Kind != KindRead || d.Audited || d.Subject != nil {
		t.Errorf("db.list = %+v, %v; want an unaudited read", d, ok)
	}
}

// A name can carry the prefix of two sites when one site's name extends the
// other's. The contract accepts it for both; handlers decide from state.
func TestDatabasePrefixIsNotOwnership(t *testing.T) {
	const db = DatabaseName("s_shop_x_main")
	if !db.BelongsTo("shop") || !db.BelongsTo("shop-x") {
		t.Error("s_shop_x_main should carry both prefixes")
	}
	wantNone := func(p Params) {
		t.Helper()
		if f := fields(t, p); f != nil {
			t.Errorf("%#v: %v", p, f)
		}
	}
	wantNone(&DbCreateParams{Site: "shop", Database: db})
	wantNone(&DbDropParams{Site: "shop", Database: db})
	got := db.ClaimedBy([]SiteName{"shop", "shop-x", "blog"})
	if !reflect.DeepEqual(got, []SiteName{"shop", "shop-x"}) {
		t.Errorf("ClaimedBy = %v, want [shop shop-x]", got)
	}
	if got := DatabaseName("s_blog_main").ClaimedBy([]SiteName{"shop"}); got != nil {
		t.Errorf("ClaimedBy = %v, want none", got)
	}
}

func TestDatabaseSize(t *testing.T) {
	checkValues(t, []DatabaseSize{0, 1, 1 << 40}, []DatabaseSize{-1})
}

func TestOwnerAmongIsTheLongestPrefix(t *testing.T) {
	for _, tc := range []struct {
		db    DatabaseName
		sites []SiteName
		want  SiteName
		ok    bool
	}{
		{"s_shop_x_main", []SiteName{"shop", "shop-x", "blog"}, "shop-x", true},
		{"s_shop_x_main", []SiteName{"shop-x", "shop"}, "shop-x", true},
		{"s_shop_main", []SiteName{"shop", "shop-x"}, "shop", true},
		{"s_shop_x_main", []SiteName{"shop"}, "shop", true},
		{"s_a_b_c_db", []SiteName{"a", "a-b", "a-b-c"}, "a-b-c", true},
		{"s_a_b_db", []SiteName{"a", "a-b", "a-b-c"}, "a-b", true},
		{"s_a_db", []SiteName{"a-b-c", "a-b", "a"}, "a", true},
		{"s_blog_main", []SiteName{"shop"}, "", false},
		{"s_blog_main", nil, "", false},
		{"s_shop_", []SiteName{"shop"}, "", false}, // nothing after the prefix
	} {
		got, ok := tc.db.OwnerAmong(tc.sites)
		if got != tc.want || ok != tc.ok {
			t.Errorf("%s among %v = %q, %v; want %q, %v", tc.db, tc.sites, got, ok, tc.want, tc.ok)
		}
	}
}

// MySQL refuses a grant whose database name is over 64 characters, and a
// grant writes each underscore as two. So db.create and db.drop refuse a
// name whose length plus its underscores is over 64.
func TestDatabaseNameMustFitAGrantEscaped(t *testing.T) {
	for name, tc := range map[string]struct {
		db DatabaseName
		ok bool
	}{
		"64 characters, 2 underscores": {"s_aa_" + DatabaseName(strings.Repeat("a", 59)), false},
		"62 characters, 2 underscores": {"s_aa_" + DatabaseName(strings.Repeat("a", 57)), true},
		"63 characters, 2 underscores": {"s_aa_" + DatabaseName(strings.Repeat("a", 58)), false},
		"many underscores, 64 escaped": {"s_aa_" + DatabaseName(strings.Repeat("a_", 18)+"aaa"), true},
		"many underscores, 65 escaped": {"s_aa_" + DatabaseName(strings.Repeat("a_", 18)+"aaaa"), false},
	} {
		for _, p := range []Params{&DbCreateParams{Site: "aa", Database: tc.db}, &DbDropParams{Site: "aa", Database: tc.db}} {
			err := p.Validate()
			if (err == nil) != tc.ok {
				t.Errorf("%s (%d characters): %#v: %v, want ok=%v", name, len(tc.db), p, err, tc.ok)
			}
		}
	}
	// The name alone is still valid for the type; the limit is the params'.
	if err := DatabaseName("s_aa_" + strings.Repeat("a", 59)).Validate(); err != nil {
		t.Errorf("Validate: %v", err)
	}
}
