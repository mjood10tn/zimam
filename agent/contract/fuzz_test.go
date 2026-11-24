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

// branchSafe is every character an accepted branch may carry into git's
// argv (always behind "refs/heads/", never a shell).
var branchSafe = regexp.MustCompile(`^[A-Za-z0-9._/-]+$`)

func FuzzBranch(f *testing.F) {
	for _, s := range []string{"main", "release/1.2", "-rf", "a..b", "refs/heads/../x", ".hidden/x", "x.lock", "a.lock/b", "a//b", "a@{1}", "main\n"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		ok := Branch(s).Validate() == nil
		if ok && (!branchSafe.MatchString(s) || len(s) > 200 || s[0] == '-' || s[0] == '/' ||
			strings.Contains(s, "..") || strings.Contains(s, "//") || strings.Contains(s, "/.") ||
			strings.HasPrefix(s, ".") || strings.HasSuffix(s, "/") || strings.HasSuffix(s, ".") || strings.HasSuffix(s, ".lock") || strings.Contains(s, ".lock/")) {
			t.Errorf("accepted %q", s)
		}
		checkSchemaAgrees(t, Branch(s), ok)
	})
}

// repositorySafe is every character an accepted repository may carry into
// the remote URL git@github.com:<owner>/<name>.git.
var repositorySafe = regexp.MustCompile(`^[A-Za-z0-9-]+/[A-Za-z0-9._-]+$`)

func FuzzRepository(f *testing.F) {
	for _, s := range []string{"octo/app", "octo/app.git", "octo/app.GIT", "octo/..", "-octo/app", "octo/app/x", "octo/a b", "octo/app\n"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		ok := Repository(s).Validate() == nil
		_, name, _ := strings.Cut(s, "/")
		if ok && (!repositorySafe.MatchString(s) || s[0] == '-' || strings.HasSuffix(strings.ToLower(s), ".git") || name == "." || name == "..") {
			t.Errorf("accepted %q", s)
		}
		checkSchemaAgrees(t, Repository(s), ok)
	})
}

// databaseSafe is every character an accepted database name may carry into
// a backquoted SQL identifier.
var databaseSafe = regexp.MustCompile(`^[a-z0-9_]+$`)

func FuzzDatabaseName(f *testing.F) {
	for _, s := range []string{"s_shop_main", "a`b", "a;b", "mysql", "A", "1a", "a\n", "ش", strings.Repeat("a", 65)} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		v := DatabaseName(s)
		ok := v.Validate() == nil
		if ok && !databaseSafe.MatchString(s) {
			t.Errorf("accepted %q", s)
		}
		checkSchemaAgrees(t, v, ok)
	})
}

// cronSafe is every character an accepted schedule renders as: digits and
// the grammar's punctuation, never a name, %, quote, newline or any byte a
// shell or cron treats specially.
var cronSafe = regexp.MustCompile(`^[0-9*/,-]+( [0-9*/,-]+){4}$`)

func FuzzCronSchedule(f *testing.F) {
	for _, s := range []string{"*/5 * * * *", "0 3 * * mon-fri", "@daily", "@reboot", "* * * * * *", "60 * * * *", "*/0 * * * *", "1- * * * *", "0 3 * * 1%x", "* * * * *\n", "0 0 * * sat-sun"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		n, err := ParseCronSchedule(s)
		if err != nil {
			if CronSchedule(s).Validate() == nil {
				t.Errorf("Parse refused %q, Validate accepted it", s)
			}
			return
		}
		if !cronSafe.MatchString(string(n)) {
			t.Errorf("accepted %q, which renders as %q", s, n)
		}
		if again, err := ParseCronSchedule(string(n)); err != nil || again != n {
			t.Errorf("%q normalises to %q, which parses to %q, %v", s, n, again, err)
		}
		checkSchemaShape(t, CronSchedule(s), true)
	})
}

func FuzzOwnerCommand(f *testing.F) {
	for _, s := range []string{"php artisan horizon", "a\nb", "a\rb", "a\x00b", "\xff", strings.Repeat("x", 4097), ""} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		ok := OwnerCommand(s).Validate() == nil
		if ok && (s == "" || len(s) > 4096 || !utf8.ValidString(s) || strings.ContainsAny(s, "\x00\r\n")) {
			t.Errorf("accepted %q", s)
		}
		checkSchemaShape(t, OwnerCommand(s), ok)
	})
}

// rcloneSafe is every character an accepted S3 field may carry into a value
// of rclone.conf (R05-D2): no space, quote, #, ; or line break.
var rcloneSafe = regexp.MustCompile(`^[A-Za-z0-9.:/_+=-]*$`)

func FuzzS3Settings(f *testing.F) {
	f.Add("https://s3.example.com", "backups", "zimam", "AKIA", "secret/key+=")
	f.Add("https://s3.example.com\n[x]", "a..b", "../x", "a b", "a\nb")
	f.Add("http://s3.example.com:9000/x", "192.168.1.1", "/abs", "k#x", "k;x")
	f.Fuzz(func(t *testing.T, endpoint, bucket, prefix, key, secret string) {
		s := S3Settings{Endpoint: S3Endpoint(endpoint), Region: "us-east-1", Bucket: S3Bucket(bucket), Prefix: BackupPath(prefix),
			AccessKeyID: S3Credential(key), SecretAccessKey: S3Credential(secret)}
		if s.Validate() != nil {
			return
		}
		for _, v := range []string{endpoint, bucket, prefix, key, secret} {
			if !rcloneSafe.MatchString(v) {
				t.Errorf("accepted %q", v)
			}
		}
		if strings.Contains(prefix, "..") || strings.HasPrefix(prefix, "/") || strings.Contains(bucket, "..") {
			t.Errorf("accepted bucket %q, prefix %q", bucket, prefix)
		}
		checkSchemaShape(t, S3Endpoint(endpoint), true)
		checkSchemaAgrees(t, S3Bucket(bucket), true)
		checkSchemaAgrees(t, BackupPath(prefix), true)
	})
}

func FuzzBackupSchedule(f *testing.F) {
	for _, s := range []string{"hourly", "daily 03:30", "weekly mon 03:30", "daily 25:00", "daily 03:30\n", "weekly Mon 03:30", "daily 3:30"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		v := BackupSchedule(s)
		ok := v.Validate() == nil
		if ok && !regexp.MustCompile(`^[a-z0-9: ]+$`).MatchString(s) {
			t.Errorf("accepted %q", s)
		}
		if ok && !regexp.MustCompile(`^[A-Za-z0-9:* -]+$`).MatchString(v.OnCalendar()) {
			t.Errorf("%q renders OnCalendar=%q", s, v.OnCalendar())
		}
		checkSchemaAgrees(t, v, ok)
	})
}

// An accepted firewall rule becomes ufw argv: its id, action and proto must be
// plain words and every source a canonical prefix (R06-D3).
func FuzzFirewallRule(f *testing.F) {
	for _, s := range []string{"app", "a b", "zimam:x", "http", "-oX"} {
		f.Add(s, "allow", "tcp", 8080, 8080, "203.0.113.0/24")
	}
	f.Add("app", "deny", "udp", 9010, 9000, "203.0.113.5/24")
	f.Add("app", "limit", "tcp", 0, 70000, "::ffff:1.2.3.4")
	f.Fuzz(func(t *testing.T, id, action, proto string, from, to int, src string) {
		r := FirewallRule{ID: FirewallRuleID(id), Action: FirewallAction(action), Proto: FirewallProto(proto), PortFrom: TCPPort(from), PortTo: TCPPort(to), From: IPList{src}}
		ok := (&FirewallRulesParams{Rules: []FirewallRule{r}}).Validate() == nil
		if !ok {
			return
		}
		if !regexp.MustCompile(`^[a-z][a-z0-9-]*$`).MatchString(id) || LockedFirewallRule(r.ID) || !regexp.MustCompile(`^(allow|deny|limit)$`).MatchString(action) || !regexp.MustCompile(`^(tcp|udp)$`).MatchString(proto) {
			t.Errorf("accepted %+v", r)
		}
		if from < 1 || to > 65535 || from > to {
			t.Errorf("accepted ports %d–%d", from, to)
		}
		if p, err := ParseIPEntry(src); err != nil || !renderedIP.MatchString(p.String()) {
			t.Errorf("accepted source %q", src)
		}
		checkSchemaAgrees(t, r.ID, true)
	})
}
