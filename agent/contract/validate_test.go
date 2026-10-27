package contract

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

// validator is a named contract type.
type validator interface {
	Validate() error
	JSONSchema() Schema
}

// checkValues asserts that every valid value passes Validate and every invalid
// one fails it, and that the type's JSONSchema() agrees with Validate on each.
func checkValues[T validator](t *testing.T, valid, invalid []T) {
	t.Helper()
	for _, v := range valid {
		if err := v.Validate(); err != nil {
			t.Errorf("%#v: Validate() = %v, want nil", v, err)
		}
		checkSchemaAgrees(t, v, true)
	}
	for _, v := range invalid {
		if err := v.Validate(); err == nil {
			t.Errorf("%#v: Validate() = nil, want an error", v)
		}
		checkSchemaAgrees(t, v, false)
	}
}

func checkSchemaAgrees(t *testing.T, v validator, want bool) {
	t.Helper()
	if !utf8.ValidString(fmt.Sprint(v)) {
		return // a JSON document can't carry invalid UTF-8
	}
	if got := schemaAccepts(t, jsonValue(t, v.JSONSchema()), jsonValue(t, v)); got != want {
		t.Errorf("%#v: schema accepts = %v, Validate accepts = %v", v, got, want)
	}
}

// jsonValue round-trips v through JSON, keeping numbers as json.Number.
func jsonValue(t *testing.T, v any) any {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal %#v: %v", v, err)
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var out any
	if err := dec.Decode(&out); err != nil {
		t.Fatalf("unmarshal %s: %v", b, err)
	}
	return out
}

// schemaAccepts applies the subset of JSON Schema the contract types use to a
// decoded JSON value. It fails the test on any keyword it doesn't know, so no
// constraint is silently skipped.
func schemaAccepts(t *testing.T, schema, v any) bool {
	t.Helper()
	s, ok := schema.(map[string]any)
	if !ok {
		t.Fatalf("schema %#v is not an object", schema)
	}
	for k, want := range s {
		switch k {
		case "type":
			var ok bool
			switch want {
			case "string":
				_, ok = v.(string)
			case "integer":
				n, isNum := v.(json.Number)
				_, err := n.Int64()
				ok = isNum && err == nil
			case "array":
				_, ok = v.([]any)
			case "object":
				_, ok = v.(map[string]any)
			default:
				t.Fatalf("unknown type %v", want)
			}
			if !ok {
				return false
			}
		case "pattern":
			if str, ok := v.(string); ok && !regexp.MustCompile(want.(string)).MatchString(str) {
				return false
			}
		case "maxLength":
			if str, ok := v.(string); ok && int64(utf8.RuneCountInString(str)) > mustInt(t, want) {
				return false
			}
		case "minimum", "maximum":
			n, ok := v.(json.Number)
			if !ok {
				continue
			}
			x, err := n.Int64()
			if err != nil {
				return false
			}
			if k == "minimum" && x < mustInt(t, want) || k == "maximum" && x > mustInt(t, want) {
				return false
			}
		case "enum":
			found := false
			for _, e := range want.([]any) {
				if fmt.Sprint(e) == fmt.Sprint(v) {
					found = true
				}
			}
			if !found {
				return false
			}
		case "not":
			if schemaAccepts(t, want, v) {
				return false
			}
		case "maxItems":
			if a, ok := v.([]any); ok && int64(len(a)) > mustInt(t, want) {
				return false
			}
		case "items":
			a, _ := v.([]any)
			for _, e := range a {
				if !schemaAccepts(t, want, e) {
					return false
				}
			}
		case "properties":
			o, _ := v.(map[string]any)
			for name, sub := range want.(map[string]any) {
				if e, ok := o[name]; ok && !schemaAccepts(t, sub, e) {
					return false
				}
			}
		case "required":
			o, _ := v.(map[string]any)
			for _, name := range want.([]any) {
				if _, ok := o[name.(string)]; !ok {
					return false
				}
			}
		case "additionalProperties":
			if want != false {
				t.Fatalf("additionalProperties %v is not supported", want)
			}
			props, _ := s["properties"].(map[string]any)
			o, _ := v.(map[string]any)
			for name := range o {
				if _, ok := props[name]; !ok {
					return false
				}
			}
		case "x-zimam-secret":
			// annotation only
		default:
			t.Fatalf("schemaAccepts: unknown keyword %q", k)
		}
	}
	return true
}

func mustInt(t *testing.T, v any) int64 {
	t.Helper()
	n, ok := v.(json.Number)
	if !ok {
		t.Fatalf("%#v is not a number", v)
	}
	x, err := n.Int64()
	if err != nil {
		t.Fatalf("%v is not an integer", n)
	}
	return x
}

func TestSiteName(t *testing.T) {
	checkValues(t,
		[]SiteName{"ab", "shop", "a-b", "a1", SiteName("a" + strings.Repeat("b", 23))},
		[]SiteName{
			"a", SiteName("a" + strings.Repeat("b", 24)), "Shop", "sh.op", "sh/op", "..",
			"-shop", "shop-", "1shop", "zimam", "panel", "default", "acme", "ش", "shop\n", "",
		})
}

func TestDomain(t *testing.T) {
	l63 := func(c string) string { return strings.Repeat(c, 63) }
	d253 := l63("a") + "." + l63("b") + "." + l63("c") + "." + strings.Repeat("d", 61)
	d254 := l63("a") + "." + l63("b") + "." + l63("c") + "." + strings.Repeat("d", 62)
	if len(d253) != 253 || len(d254) != 254 {
		t.Fatalf("test names have lengths %d and %d", len(d253), len(d254))
	}
	checkValues(t,
		[]Domain{"shop.example.com", "xn--bcher-kva.example", "a.io", Domain(d253), "a.xn--p1ai"},
		[]Domain{
			"localhost", "Shop.example.com", "*.example.com", "shop example.com", "shop;.com",
			"shop{.com", "shop\n.com", "$x.com", `"x".com`, "-a.com", "a-.com", "a.c0m",
			Domain(d254), Domain(strings.Repeat("a", 64) + ".com"), "a..com", "", "a.xn--", "a.com\n",
		})
}

func TestPHPVersion(t *testing.T) {
	checkValues(t,
		[]PHPVersion{"8.2", "8.3", "8.4", "8.5"},
		[]PHPVersion{"8.5; rm -rf /", "../8.4", "8.1", "8.6", "8", ""})
}

func TestPHPSupportedOn(t *testing.T) {
	r, ok := PHPVersion("8.2").Release()
	if !ok {
		t.Fatal("8.2 is not in the allowlist")
	}
	if !r.SupportedOn(time.Date(2026, 12, 31, 23, 59, 0, 0, time.UTC)) {
		t.Error("8.2 should be supported on 2026-12-31 23:59Z")
	}
	if r.SupportedOn(time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)) {
		t.Error("8.2 should not be supported on 2027-01-01 00:00Z")
	}
}

func TestIPList(t *testing.T) {
	valid := IPList{"10.0.0.0/8", "2001:db8::/32", "192.0.2.7"}
	if err := valid.Validate(); err != nil {
		t.Errorf("%q: Validate() = %v", valid, err)
	}
	if !schemaAccepts(t, jsonValue(t, valid.JSONSchema()), jsonValue(t, valid)) {
		t.Errorf("schema rejects %q", valid)
	}
	many := make(IPList, 101)
	for i := range many {
		many[i] = fmt.Sprintf("10.0.%d.0/24", i)
	}
	if err := many.Validate(); err == nil {
		t.Error("101 entries: Validate() = nil")
	}
	if schemaAccepts(t, jsonValue(t, many.JSONSchema()), jsonValue(t, many)) {
		t.Error("schema accepts 101 entries")
	}
	for _, e := range []string{
		"fe80::1%eth0", "10.0.0.1/8", "1.2.3.4/33", "::1/129", "::ffff:1.2.3.4", "1.2.3", "10.0.0.0/8 ", "",
	} {
		if err := (IPList{e}).Validate(); err == nil {
			t.Errorf("%q: Validate() = nil", e)
		}
	}
	err := IPList{"10.0.0.1/8"}.Validate()
	if err == nil || !strings.Contains(err.Error(), "10.0.0.0/8") {
		t.Errorf("10.0.0.1/8: error %v does not suggest 10.0.0.0/8", err)
	}
}

func TestParseIPEntryCanonical(t *testing.T) {
	for in, want := range map[string]string{
		"192.0.2.7":     "192.0.2.7/32",
		"2001:db8::/32": "2001:db8::/32",
		"2001:DB8::1":   "2001:db8::1/128",
	} {
		p, err := ParseIPEntry(in)
		if err != nil || p.String() != want {
			t.Errorf("ParseIPEntry(%q) = %v, %v; want %s", in, p, err, want)
		}
	}
}

func TestBasicAuthUsers(t *testing.T) {
	hash := "$2y$12$" + strings.Repeat("a", 53)
	user := func(name, h string) BasicAuthUsers { return BasicAuthUsers{{Username: name, PasswordHash: h}} }

	valid := BasicAuthUsers{{Username: "Alice.B_c-1", PasswordHash: hash}, {Username: "bob", PasswordHash: "$2y$10$" + strings.Repeat("./Az09", 8) + "abcde"}}
	if err := valid.Validate(); err != nil {
		t.Errorf("Validate() = %v", err)
	}
	if !schemaAccepts(t, jsonValue(t, valid.JSONSchema()), jsonValue(t, valid)) {
		t.Error("schema rejects valid users")
	}

	many := make(BasicAuthUsers, 21)
	for i := range many {
		many[i] = BasicAuthUser{Username: fmt.Sprintf("u%d", i), PasswordHash: hash}
	}
	for name, u := range map[string]BasicAuthUsers{
		"colon":          user("a:b", hash),
		"newline":        user("ab\n", hash),
		"empty username": user("", hash),
		"33 characters":  user(strings.Repeat("a", 33), hash),
		"21 users":       many,
		"duplicate":      {{Username: "a", PasswordHash: hash}, {Username: "a", PasswordHash: hash}},
		"$2a$":           user("a", "$2a$12$"+strings.Repeat("a", 53)),
		"cost 09":        user("a", "$2y$09$"+strings.Repeat("a", 53)),
		"cost 15":        user("a", "$2y$15$"+strings.Repeat("a", 53)),
		"52 characters":  user("a", "$2y$12$"+strings.Repeat("a", 52)),
	} {
		if err := u.Validate(); err == nil {
			t.Errorf("%s: Validate() = nil", name)
		}
		if name != "duplicate" && schemaAccepts(t, jsonValue(t, u.JSONSchema()), jsonValue(t, u)) {
			t.Errorf("%s: schema accepts it", name)
		}
	}
}

func TestBasicAuthUsersSchemaMarksSecret(t *testing.T) {
	s := jsonValue(t, BasicAuthUsers(nil).JSONSchema()).(map[string]any)
	hash := s["items"].(map[string]any)["properties"].(map[string]any)["password_hash"].(map[string]any)
	if hash["x-zimam-secret"] != true {
		t.Errorf("password_hash schema = %v, want x-zimam-secret", hash)
	}
}

func TestHSTSMaxAge(t *testing.T) {
	checkValues(t,
		[]HSTSMaxAge{86400, 2592000, 31536000},
		[]HSTSMaxAge{0, -1, 86399, 63072000})
}

func TestJobID(t *testing.T) {
	id := "01JBX7Q2M5YQ8T3N6V0R4K9W1E"
	checkValues(t,
		[]JobID{JobID(id), "7ZZZZZZZZZZZZZZZZZZZZZZZZZ"},
		[]JobID{
			JobID(strings.ToLower(id)), JobID("I" + id[1:]), JobID("L" + id[1:]),
			JobID("O" + id[1:]), JobID("U" + id[1:]), JobID(id[:25]), JobID(id + "0"), "", JobID(id + "\n"),
		})
}

func TestJobState(t *testing.T) {
	checkValues(t,
		[]JobState{JobQueued, JobRunning, JobSucceeded, JobFailed, JobCancelled},
		[]JobState{"", "waiting", "Running", "done"})
}

func TestLogOffset(t *testing.T) {
	checkValues(t, []LogOffset{0, 1, 1 << 40}, []LogOffset{-1, -1 << 40})
}

func TestBuild(t *testing.T) {
	checkValues(t, []Build{BuildRelease, BuildE2E}, []Build{"", "debug", "Release"})
}

func TestLogSource(t *testing.T) {
	valid := []LogSource{"journal.php-8.4", "journal.php-8.2"}
	for _, s := range fixedLogSources {
		valid = append(valid, LogSource(s))
	}
	checkValues(t, valid, []LogSource{"journal.php-8.1", "/var/log/syslog", "", "journal.php-", "site.", "journal.php-8.4\n"})
	if !LogSource("site.php-error").IsSite() || LogSource("journal.system").IsSite() {
		t.Error("IsSite is wrong")
	}
}

func TestArchiveName(t *testing.T) {
	checkValues(t,
		[]ArchiveName{"access.log-20261025-1761350400.gz", "php-error.log-20261025-1.gz"},
		[]ArchiveName{"../../../../etc/shadow", "/etc/shadow", "Access.log-20261025-1.gz", "access.log-20261025-1.gz/x", "", "access.log-20261025-1.gz\n"})
}

func TestLogSearch(t *testing.T) {
	checkValues(t,
		[]LogSearch{"", "error 500", LogSearch(strings.Repeat("ش", 200))},
		[]LogSearch{LogSearch(strings.Repeat("ش", 201)), "\xff", "a\x00b", "a\nb", "a\u0085b"})
}

func TestRetentionDays(t *testing.T) {
	checkValues(t, []RetentionDays{1, 14, 365}, []RetentionDays{0, -1, 366})
}

func TestSiteCapBytes(t *testing.T) {
	checkValues(t,
		[]SiteCapBytes{SiteCapBytes(minSiteCapBytes), SiteCapBytes(100 * GiB)},
		[]SiteCapBytes{SiteCapBytes(minSiteCapBytes - 1), SiteCapBytes(100*GiB + 1), 0, -1})
}

func TestFieldErrors(t *testing.T) {
	f := FieldErrors{}
	f.Check("name", SiteName("shop"))
	if err := f.Err(); err != nil {
		t.Fatalf("Err() = %v, want nil", err)
	}
	f.Check("name", SiteName("Shop"))
	f.Check("domain", Domain("a.io"))
	err := f.Err()
	ae, ok := err.(*AgentError)
	if !ok || ae.Code != ErrValidationFailed || len(ae.Fields) != 1 || ae.Fields["name"] == "" {
		t.Fatalf("Err() = %#v, want validation_failed with one field, name", err)
	}
}
