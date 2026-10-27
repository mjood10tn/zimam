package contract

import (
	"encoding/json"
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"
)

// templateSafe is every character an accepted site name, domain or archive
// name may carry into a template, a unit name or a path.
var templateSafe = regexp.MustCompile(`^[a-z0-9.-]+$`)

func FuzzDecodeRequest(f *testing.F) {
	for _, s := range []string{
		validRequest, `{"v":1,"v":2}`, `{"V":1}`, `{} {}`, `null`,
		`{"v":1,"params":{"users":[{"username":"a","username":"b"}]}}`,
	} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		var req Request
		if DecodeStrict(data, &req) == nil && (!utf8.Valid(data) || !json.Valid(data)) {
			t.Errorf("accepted %q, which is not valid UTF-8 JSON", data)
		}
	})
}

// fuzzTemplateSafe checks that an accepted value has only template-safe
// characters and that the type's schema agrees with Validate.
func fuzzTemplateSafe(t *testing.T, v validator, s string) {
	t.Helper()
	ok := v.Validate() == nil
	if ok && !templateSafe.MatchString(s) {
		t.Errorf("accepted %q", s)
	}
	checkSchemaAgrees(t, v, ok)
}

func FuzzSiteName(f *testing.F) {
	for _, s := range []string{"shop", "a-b", "Shop", "sh.op", "zimam", "shop\n", "ش"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) { fuzzTemplateSafe(t, SiteName(s), s) })
}

func FuzzDomain(f *testing.F) {
	for _, s := range []string{"shop.example.com", "xn--bcher-kva.example", "a.c0m", "*.example.com", "a..com", "a.xn--"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) { fuzzTemplateSafe(t, Domain(s), s) })
}

func FuzzArchiveName(f *testing.F) {
	for _, s := range []string{"access.log-20261025-1761350400.gz", "../../../../etc/shadow", "access.log-20261025-1.gz/x"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) { fuzzTemplateSafe(t, ArchiveName(s), s) })
}

// renderedIP is every character a parsed IP entry renders as.
var renderedIP = regexp.MustCompile(`^[0-9a-f.:]+/[0-9]{1,3}$`)

func FuzzIPList(f *testing.F) {
	for _, s := range []string{"10.0.0.0/8", "2001:db8::/32", "192.0.2.7", "fe80::1%eth0", "10.0.0.1/8", "::ffff:1.2.3.4"} {
		f.Add(s, "192.0.2.0/24")
	}
	f.Fuzz(func(t *testing.T, a, b string) {
		if (IPList{a, b}).Validate() != nil {
			return
		}
		for _, e := range []string{a, b} {
			p, err := ParseIPEntry(e)
			if err != nil || p != p.Masked() || p.Addr().Zone() != "" || p.Addr().Is4In6() || !renderedIP.MatchString(p.String()) {
				t.Errorf("accepted %q as %v (%v), which is not canonical", e, p, err)
			}
		}
	})
}

func FuzzBasicAuthUsers(f *testing.F) {
	f.Add("alice", "$2y$12$"+strings.Repeat("a", 53))
	f.Add("a:b", "$2a$12$"+strings.Repeat("a", 53))
	f.Fuzz(func(t *testing.T, name, hash string) {
		u := BasicAuthUsers{{Username: name, PasswordHash: hash}}
		if u.Validate() == nil && strings.ContainsAny(name+hash, ":\r\n") {
			t.Errorf("accepted %q / %q, which would break an htpasswd line", name, hash)
		}
	})
}

func FuzzLogSearch(f *testing.F) {
	for _, s := range []string{"error 500", "a\x00b", "a\nb", "\xff", strings.Repeat("ش", 201)} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) { checkSchemaAgrees(t, LogSearch(s), LogSearch(s).Validate() == nil) })
}
