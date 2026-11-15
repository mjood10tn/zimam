package contract

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestRepository(t *testing.T) {
	checkValues(t,
		[]Repository{
			"octo/app", "Octo-Cat/my.app", "a/b", "o/x_y-z.1", "0/.github", "o/..x", "o/x.gitx",
			Repository(strings.Repeat("a", 39) + "/" + strings.Repeat("b", 100)),
		},
		[]Repository{
			"", "octo", "octo/", "/app", "-octo/app", "octo/app.git", "octo/.", "octo/..",
			"oc.to/app", "oc_to/app", "octo/app/x", "octo/a b", "octo/app\n", "octo/app;rm",
			"https://github.com/octo/app", "git@github.com:octo/app",
			Repository(strings.Repeat("a", 40) + "/app"),
			Repository("octo/" + strings.Repeat("b", 101)),
		})
}

func TestBranch(t *testing.T) {
	checkValues(t,
		[]Branch{
			"main", "release/1.2", "feature/x_y-z", "v1.0", "a", "a.b/c.d", "x.locks", "lock",
			Branch(strings.Repeat("a", 200)),
		},
		[]Branch{
			"", "-rf", "/main", "main/", "main.", "a..b", "refs/heads/../x", "a//b",
			".hidden/x", "x/.hidden", ".x", "x.lock", "a.lock/b", "a@{1}", "a b", "a~1", "a^", "a:b",
			"a?", "a*", "a[", `a\b`, "main\n", "ش",
			Branch(strings.Repeat("a", 201)),
		})
}

func TestCommitSHA(t *testing.T) {
	sha := "0123456789abcdef0123456789abcdef01234567"
	checkValues(t,
		[]CommitSHA{CommitSHA(sha), CommitSHA(strings.Repeat("f", 40))},
		[]CommitSHA{
			"", CommitSHA(strings.ToUpper(sha)), CommitSHA(sha[:39]), CommitSHA(sha + "0"),
			CommitSHA(sha[:7]), CommitSHA(sha + "\n"), CommitSHA("g" + sha[1:]), "HEAD", "main",
		})
}

func TestReleaseID(t *testing.T) {
	checkValues(t,
		[]ReleaseID{"20261115093000-0123abc", "00000000000000-0000000"},
		[]ReleaseID{
			"", "initial", "20261115093000-0123ABC", "2026111509300-0123abc", "20261115093000-0123ab",
			"20261115093000-0123abcd", "20261115093000_0123abc", "20261115093000-0123abc\n",
			"../20261115093000-0123abc",
		})
}

func TestDeployTrigger(t *testing.T) {
	checkValues(t,
		[]DeployTrigger{DeployManual, DeployWebhook, DeployURL},
		[]DeployTrigger{"", "Manual", "cron", "url\n"})
}

func TestDeployScript(t *testing.T) {
	checkValues(t,
		[]DeployScript{"", "php artisan migrate --force\n", "echo 'مرحبا'\n", DeployScript(strings.Repeat("a", 65536))},
		[]DeployScript{"a\x00b", DeployScript(strings.Repeat("a", 65537))})
	// The schema counts characters, Validate bytes: 65,536 two-byte
	// characters pass the schema's maxLength but not Validate.
	if DeployScript(strings.Repeat("ش", 32769)).Validate() == nil {
		t.Error("65,538 bytes passed")
	}
	if DeployScript("\xff").Validate() == nil {
		t.Error("invalid UTF-8 passed")
	}
}

func TestEnvContent(t *testing.T) {
	checkValues(t,
		[]EnvContent{"", "APP_ENV=production\nAPP_KEY=base64:abc=\n", EnvContent(strings.Repeat("a", 65536))},
		[]EnvContent{"APP_KEY=a\x00", EnvContent(strings.Repeat("a", 65537))})
	if EnvContent(strings.Repeat("ش", 32769)).Validate() == nil {
		t.Error("65,538 bytes passed")
	}
	if EnvContent("A=\xff").Validate() == nil {
		t.Error("invalid UTF-8 passed")
	}
}

// fields returns Validate's field messages, or nil when it passes.
func fields(t *testing.T, p Params) FieldErrors {
	t.Helper()
	err := p.Validate()
	if err == nil {
		return nil
	}
	var ae *AgentError
	if !errors.As(err, &ae) || ae.Code != ErrValidationFailed {
		t.Fatalf("Validate = %v, want validation_failed", err)
	}
	return ae.Fields
}

// wantRefused checks that p fails Validate with a message for each field.
func wantRefused(t *testing.T, p Params, names ...string) {
	t.Helper()
	f := fields(t, p)
	for _, n := range names {
		if f[n] == "" {
			t.Errorf("%#v: fields = %v, want %s refused", p, f, n)
		}
	}
}

func TestSiteRepoParams(t *testing.T) {
	for _, p := range []*SiteRepoParams{
		{Name: "shop", Repository: "octo/app", Branch: "main"},
		{Name: "shop", Repository: "octo/app", Branch: "release/2", RotateKey: true},
	} {
		if f := fields(t, p); f != nil {
			t.Errorf("%+v: %v", p, f)
		}
	}
	wantRefused(t, &SiteRepoParams{Name: "Shop", Repository: "octo/app.git", Branch: "-rf"}, "name", "repository", "branch")
	wantRefused(t, &SiteRepoParams{Name: "shop"}, "repository", "branch")
}

func TestSiteDeployParams(t *testing.T) {
	sha := CommitSHA(strings.Repeat("a", 40))
	for _, p := range []*SiteDeployParams{
		{Name: "shop", Trigger: DeployManual},
		{Name: "shop", Commit: sha, Script: "php artisan migrate --force", Composer: true, Trigger: DeployWebhook},
		{Name: "shop", Trigger: DeployURL, Script: DeployScript(strings.Repeat("a", 65536))},
	} {
		if f := fields(t, p); f != nil {
			t.Errorf("%+v: %v", p, f)
		}
	}
	wantRefused(t, &SiteDeployParams{Name: "Shop", Commit: "ABC", Script: "a\x00", Trigger: "cron"}, "name", "commit", "script", "trigger")
	wantRefused(t, &SiteDeployParams{Name: "shop"}, "trigger")
	wantRefused(t, &SiteDeployParams{Name: "shop", Trigger: DeployManual, Script: DeployScript(strings.Repeat("a", 65537))}, "script")
}

func TestSiteRollbackParams(t *testing.T) {
	if f := fields(t, &SiteRollbackParams{Name: "shop", Release: "20261115093000-0123abc"}); f != nil {
		t.Errorf("valid rollback: %v", f)
	}
	wantRefused(t, &SiteRollbackParams{Name: "Shop", Release: "initial"}, "name", "release")
	wantRefused(t, &SiteRollbackParams{Name: "shop"}, "release")
}

func TestSiteEnvWriteParams(t *testing.T) {
	if f := fields(t, &SiteEnvWriteParams{Name: "shop", Content: "APP_ENV=production\n"}); f != nil {
		t.Errorf("valid .env: %v", f)
	}
	// An empty .env is a valid file.
	if f := fields(t, &SiteEnvWriteParams{Name: "shop"}); f != nil {
		t.Errorf("empty .env: %v", f)
	}
	wantRefused(t, &SiteEnvWriteParams{Name: "Shop", Content: "A=\x00"}, "name", "content")
	wantRefused(t, &SiteEnvWriteParams{Name: "shop", Content: EnvContent(strings.Repeat("a", 65537))}, "content")
}

// secretField reports whether t's field with the given json name is tagged
// secret:"true", which the audit log and the schemas both read.
func secretField(t *testing.T, typ reflect.Type, name string) bool {
	t.Helper()
	for i := range typ.NumField() {
		f := typ.Field(i)
		if n, _, _ := strings.Cut(f.Tag.Get("json"), ","); n == name {
			return f.Tag.Get("secret") == "true"
		}
	}
	t.Fatalf("%s has no field %q", typ, name)
	return false
}

// A .env holds the site's secrets: both the read's result and the write's
// params mark it, so the audit log and agent_actions redact it (R02-D10).
func TestEnvReadResultIsSecret(t *testing.T) {
	read, ok := Lookup("site.env.read")
	if !ok {
		t.Fatal("site.env.read isn't registered")
	}
	if !secretField(t, reflect.TypeOf(read.Result), "content") {
		t.Error("site.env.read's result content isn't secret")
	}
	write, ok := Lookup("site.env.write")
	if !ok {
		t.Fatal("site.env.write isn't registered")
	}
	if !secretField(t, reflect.TypeOf(write.Params()).Elem(), "content") {
		t.Error("site.env.write's content isn't secret")
	}
	// The deploy script is audited as it is (R02-D2).
	deploy, ok := Lookup("site.deploy")
	if !ok {
		t.Fatal("site.deploy isn't registered")
	}
	if secretField(t, reflect.TypeOf(deploy.Params()).Elem(), "script") {
		t.Error("site.deploy's script is marked secret; R02-D2 audits it")
	}
}

func TestDeployCommandsSubjects(t *testing.T) {
	for name, p := range map[string]Params{
		"site.repo":      &SiteRepoParams{Name: "shop"},
		"site.deploy":    &SiteDeployParams{Name: "shop"},
		"site.rollback":  &SiteRollbackParams{Name: "shop"},
		"site.env.write": &SiteEnvWriteParams{Name: "shop"},
	} {
		d, ok := Lookup(name)
		if !ok {
			t.Errorf("%s isn't registered", name)
			continue
		}
		if got := d.Subject(p); got != "site:shop" {
			t.Errorf("%s: subject %q, want site:shop", name, got)
		}
	}
}
