package contract

import (
	"errors"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// designCommands is the §5.3 table: every command the design names, with its
// kind, job subject ("" for –; <…> stands for a value from the params) and
// whether it is audited. registered marks the commands the agent registers
// so far; the task that adds a command's descriptor sets it.
var designCommands = []struct {
	name       string
	kind       Kind
	subject    string
	audited    bool
	registered bool
}{
	{"agent.version", KindRead, "", false, true},
	{"system.status", KindRead, "", false, true},
	{"system.metrics", KindRead, "", false, false},
	{"php.install", KindJob, "php:<version>", true, true},
	{"php.uninstall", KindJob, "php:<version>", true, true},
	{"php.default", KindJob, "server", true, true},
	{"site.provision", KindJob, "site:<name>", true, true},
	{"site.php", KindJob, "site:<name>", true, true},
	{"site.security", KindJob, "site:<name>", true, false},
	{"site.destroy", KindJob, "site:<name>", true, true},
	{"cert.issue", KindJob, "site:<name>", true, true},
	{"security.check", KindJob, "server or site:<name>", true, false},
	{"logs.list", KindRead, "", false, false},
	{"logs.read", KindRead, "", false, false},
	{"logs.policy", KindJob, "server", true, false},
	{"job.status", KindRead, "", false, true},
	{"job.follow", KindRead, "", false, true},
	{"job.cancel", KindDirect, "", true, true},
}

// subjectPattern turns a §5.3 subject such as "site:<name>" or
// "server or site:<name>" into a regexp in which <…> matches any value.
func subjectPattern(s string) *regexp.Regexp {
	alts := strings.Split(s, " or ")
	for i, a := range alts {
		alts[i] = regexp.MustCompile(`<[a-z_]+>`).ReplaceAllString(regexp.QuoteMeta(a), `[^:]*`)
	}
	return regexp.MustCompile(`^(` + strings.Join(alts, "|") + `)$`)
}

func TestDescriptorsMatchDesign(t *testing.T) {
	if len(designCommands) != 18 {
		t.Fatalf("the table has %d rows, want all 18 of §5.3", len(designCommands))
	}
	rows := map[string]int{}
	for i, row := range designCommands {
		rows[row.name] = i
	}
	registered := map[string]bool{}
	for _, d := range All() {
		registered[d.Name] = true
		i, ok := rows[d.Name]
		if !ok {
			t.Errorf("%s is registered but isn't in §5.3", d.Name)
			continue
		}
		row := designCommands[i]
		if d.Kind != row.kind {
			t.Errorf("%s: kind %q, want %q", d.Name, d.Kind, row.kind)
		}
		if d.Audited != row.audited {
			t.Errorf("%s: audited %v, want %v", d.Name, d.Audited, row.audited)
		}
		if d.E2EOnly {
			t.Errorf("%s: a §5.3 command is E2EOnly", d.Name)
		}
		switch {
		case row.subject == "" && d.Subject != nil:
			t.Errorf("%s: has a Subject, but §5.3 gives it none", d.Name)
		case row.subject != "" && d.Subject == nil:
			t.Errorf("%s: has no Subject, want %q", d.Name, row.subject)
		case row.subject != "":
			if got := d.Subject(d.Params()); !subjectPattern(row.subject).MatchString(got) {
				t.Errorf("%s: subject %q doesn't match %q", d.Name, got, row.subject)
			}
		}
	}
	for _, row := range designCommands {
		if registered[row.name] != row.registered {
			t.Errorf("%s: registered = %v, want %v", row.name, registered[row.name], row.registered)
		}
	}
}

func TestSubjectPattern(t *testing.T) {
	re := subjectPattern("server or site:<name>")
	for s, want := range map[string]bool{"server": true, "site:shop": true, "site:": true, "php:8.4": false, "site:a:b": false, "servers": false} {
		if re.MatchString(s) != want {
			t.Errorf("%q: match = %v, want %v", s, !want, want)
		}
	}
}

func TestLookupAndAll(t *testing.T) {
	if d, ok := Lookup("job.status"); !ok || d.Name != "job.status" {
		t.Errorf(`Lookup("job.status") = %q, %v`, d.Name, ok)
	}
	if _, ok := Lookup("job.unknown"); ok {
		t.Error(`Lookup("job.unknown") found a command`)
	}
	all := All()
	if !slices.IsSortedFunc(all, func(a, b Descriptor) int { return strings.Compare(a.Name, b.Name) }) {
		t.Error("All() is not sorted by name")
	}
}

func TestRegisterRejectsBadDescriptors(t *testing.T) {
	params := func() Params { return &JobStatusParams{} }
	subject := func(Params) string { return "server" }
	cases := map[string]Descriptor{
		"duplicate name": {Name: "job.status", Kind: KindRead, Params: params, Result: JobStatusResult{}},
		"empty name":     {Kind: KindRead, Params: params, Result: JobStatusResult{}},
		"bad name":       {Name: "../job.status", Kind: KindRead, Params: params, Result: JobStatusResult{}},
		"params value":   {Name: "test.value", Kind: KindRead, Params: func() Params { return JobStatusParams{} }, Result: JobStatusResult{}},
		"unknown kind":   {Name: "test.kind", Kind: "write", Params: params, Result: JobStatusResult{}},
		"no params":      {Name: "test.params", Kind: KindRead, Result: JobStatusResult{}},
		"no result":      {Name: "test.result", Kind: KindRead, Params: params},
		"job no subject": {Name: "test.job", Kind: KindJob, Params: params, Result: JobStatusResult{}},
		"read subject":   {Name: "test.read", Kind: KindRead, Params: params, Result: JobStatusResult{}, Subject: subject},
	}
	before := len(All())
	for name, d := range cases {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Error("Register accepted it")
				}
			}()
			Register(d)
		})
	}
	if after := len(All()); after != before {
		t.Errorf("rejected descriptors were registered: %d commands, want %d", after, before)
	}
}

// mapOrInterface returns the path of the first map or interface inside t, or "".
func mapOrInterface(t reflect.Type, path string, seen map[reflect.Type]bool) string {
	if seen[t] {
		return ""
	}
	seen[t] = true
	switch t.Kind() {
	case reflect.Map, reflect.Interface:
		return path
	case reflect.Pointer, reflect.Slice, reflect.Array:
		return mapOrInterface(t.Elem(), path+"[]", seen)
	case reflect.Struct:
		for i := range t.NumField() {
			f := t.Field(i)
			if p := mapOrInterface(f.Type, path+"."+f.Name, seen); p != "" {
				return p
			}
		}
	}
	return ""
}

func TestParamsHaveNoMaps(t *testing.T) {
	type hidden struct {
		Users []struct{ Env map[string]string }
	}
	if p := mapOrInterface(reflect.TypeFor[hidden](), "hidden", map[reflect.Type]bool{}); p != "hidden.Users[].Env" {
		t.Fatalf("mapOrInterface missed a nested map: %q", p)
	}
	for _, d := range All() {
		if p := mapOrInterface(reflect.TypeOf(d.Params()), d.Name, map[reflect.Type]bool{}); p != "" {
			t.Errorf("%s params hold a map or interface at %s", d.Name, p)
		}
	}
}

func TestEveryParamsValidates(t *testing.T) {
	for _, d := range All() {
		t.Run(d.Name, func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("Validate on zero params panicked: %v", r)
				}
			}()
			err := d.Params().Validate()
			if err == nil {
				return
			}
			var ae *AgentError
			if !errors.As(err, &ae) || ae.Code != ErrValidationFailed || len(ae.Fields) == 0 {
				t.Errorf("zero params: Validate() = %v, want nil or a FieldErrors error", err)
			}
		})
	}
}
