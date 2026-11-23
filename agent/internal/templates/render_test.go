package templates

import (
	"fmt"
	"net/netip"
	"strings"
	"testing"
	"testing/fstest"
	"text/template"

	"zimam/agent/contract"
)

// testSet parses templates the way the embedded set is parsed, from a map of
// template name to body.
func testSet(t testing.TB, files map[string]string) *template.Template {
	t.Helper()
	fsys := fstest.MapFS{}
	for name, body := range files {
		fsys[name+".tmpl"] = &fstest.MapFile{Data: []byte(body)}
	}
	set, err := parse(fsys)
	if err != nil {
		t.Fatal(err)
	}
	return set
}

func TestRenderMissingKeyFails(t *testing.T) {
	set := testSet(t, map[string]string{
		"test/missing.conf": "server_name {{.domain}};\n",
		"test/outer.conf":   `{{template "test/missing.conf" .}}`,
	})
	for _, name := range []string{"test/missing.conf", "test/outer.conf"} {
		if out, err := render(set, name, map[string]any{"other": "x"}); err == nil {
			t.Errorf("%s rendered %q with its key missing", name, out)
		}
	}
	out, err := render(set, "test/outer.conf", map[string]any{"domain": "shop.example.com"})
	if err != nil || string(out) != "server_name shop.example.com;\n" {
		t.Errorf("with the key present: %q, %v", out, err)
	}
}

func TestRenderUnknownName(t *testing.T) {
	if out, err := Render("nginx/no-such.conf", nil); err == nil {
		t.Errorf("rendered an unknown template: %q", out)
	}
}

func TestSafeRejectsInjection(t *testing.T) {
	set := testSet(t, map[string]string{"test/safe.conf": "server_name {{safe .}};\n"})
	for _, bad := range []string{"a;b", "a{b", "a}b", "a\nb", "a b", "a\tb", "$host", `a"b`, "a'b", "a#b", ""} {
		if out, err := render(set, "test/safe.conf", bad); err == nil {
			t.Errorf("safe rendered %q as %q", bad, out)
		}
	}
	for _, good := range []any{"shop.example.com", "https://packages.sury.org/php/", "8.4", "deb@sury.org", contract.Domain("shop.example.com"), contract.SiteName("shop")} {
		out, err := render(set, "test/safe.conf", good)
		if want := "server_name " + fmt.Sprint(good) + ";\n"; err != nil || string(out) != want {
			t.Errorf("safe(%q) = %q, %v; want %q", good, out, err, want)
		}
	}
	if out, err := render(set, "test/safe.conf", 8080); err == nil {
		t.Errorf("safe rendered a non-string: %q", out)
	}
}

func TestPrefixRendersParsedEntries(t *testing.T) {
	set := testSet(t, map[string]string{"test/prefix.conf": "allow {{prefix .}};\n"})
	for entry, want := range map[string]string{
		"192.0.2.0/24":  "allow 192.0.2.0/24;\n",
		"2001:db8::/32": "allow 2001:db8::/32;\n",
		"192.0.2.7":     "allow 192.0.2.7/32;\n",
		"2001:db8::1":   "allow 2001:db8::1/128;\n",
	} {
		p, err := contract.ParseIPEntry(entry)
		if err != nil {
			t.Fatal(err)
		}
		if out, err := render(set, "test/prefix.conf", p); err != nil || string(out) != want {
			t.Errorf("prefix(%s) = %q, %v; want %q", entry, out, err, want)
		}
	}
	for _, bad := range []any{
		netip.MustParsePrefix("192.0.2.7/24"), // host bits set
		netip.MustParsePrefix("::ffff:192.0.2.0/120"),
		netip.Prefix{},
		"192.0.2.0/24", // raw input, not a parsed prefix
	} {
		if out, err := render(set, "test/prefix.conf", bad); err == nil {
			t.Errorf("prefix(%v) rendered %q", bad, out)
		}
	}
}

func TestHtpasswdLineRevalidates(t *testing.T) {
	set := testSet(t, map[string]string{"test/htpasswd": "{{range .}}{{htpasswdLine .}}\n{{end}}"})
	hash := "$2y$12$" + strings.Repeat("a", 53)
	out, err := render(set, "test/htpasswd", []contract.BasicAuthUser{{Username: "alice", PasswordHash: hash}})
	if want := "alice:" + hash + "\n"; err != nil || string(out) != want {
		t.Errorf("htpasswdLine = %q, %v; want %q", out, err, want)
	}
	for _, bad := range []contract.BasicAuthUser{
		{Username: "ali:ce", PasswordHash: hash},
		{Username: "alice\nbob", PasswordHash: hash},
		{Username: "alice", PasswordHash: "plain"},
		{Username: "alice", PasswordHash: hash + "\nbob:" + hash},
	} {
		if out, err := render(set, "test/htpasswd", []contract.BasicAuthUser{bad}); err == nil {
			t.Errorf("htpasswdLine rendered %+v as %q", bad, out)
		}
	}
}

// A schedule reaches the cron file only in its normal form: the template
// refuses a stored one that isn't, so nothing but the parser's own output is
// ever written (R04-D5).
func TestCronScheduleRendersOnlyNormalForm(t *testing.T) {
	for s, ok := range map[string]bool{
		"0 3 * * 1": true, "*/15 1-5,7 * 1-3 *": true, "0 0 * * *": true,
		"0 3 * * MON": false, "@daily": false, "0 3  * * 1": false, "0 3 * * 1\n* * * * * root x": false, "": false, "61 * * * *": false,
	} {
		_, err := Render("cron/site.cron", SiteCron{Site: "shop", Jobs: []CronLine{{ID: "a", Schedule: contract.CronSchedule(s)}}})
		if (err == nil) != ok {
			t.Errorf("schedule %q: err = %v, want ok = %v", s, err, ok)
		}
	}
	if _, err := Render("cron/site.cron", SiteCron{Site: "sh.op"}); err == nil {
		t.Error("a site name with a dot rendered: cron would ignore the file")
	}
}

// A backup unit renders only a valid schedule and plan: a stored value that
// isn't one never reaches OnCalendar= or ExecStart= (R05-D7).
func TestBackupUnitRendersOnlyValidValues(t *testing.T) {
	for _, u := range []BackupUnit{
		{Plan: "nightly", Schedule: "daily 25:00"},
		{Plan: "nightly", Schedule: "hourly\nExecStart=/bin/sh"},
		{Plan: "night ly", Schedule: "hourly"},
		{Plan: "nightly\nUser=root", Schedule: "hourly"},
	} {
		if out, err := Render("systemd/zimam-backup.timer", u); err == nil {
			t.Errorf("%+v rendered the timer:\n%s", u, out)
		}
		if u.Plan.Validate() != nil {
			if out, err := Render("systemd/zimam-backup.service", u); err == nil {
				t.Errorf("%+v rendered the service:\n%s", u, out)
			}
		}
	}
}
