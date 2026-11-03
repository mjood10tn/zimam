package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"zimam/agent/contract"
)

type apiKey struct {
	ID     string `json:"id"`
	Secret string `json:"secret" secret:"true"`
}

// secretParams holds secrets at the top level, in a slice of structs, behind
// a pointer, and inside a named type that supplies its own JSONSchema().
type secretParams struct {
	Name   contract.SiteName       `json:"name"`
	Token  string                  `json:"token" secret:"true"`
	Users  contract.BasicAuthUsers `json:"users"`
	Keys   []apiKey                `json:"keys,omitempty"`
	Backup *apiKey                 `json:"backup"`
}

func (secretParams) Validate() error { return nil }

// richResult uses every Go shape the generator maps.
type richResult struct {
	Count    uint32             `json:"count"`
	Ratio    float64            `json:"ratio"`
	OK       bool               `json:"ok"`
	When     time.Time          `json:"when"`
	Maybe    *time.Time         `json:"maybe"`
	Raw      json.RawMessage    `json:"raw"`
	Extra    any                `json:"extra"`
	Labels   map[string]string  `json:"labels,omitempty"`
	Keys     []apiKey           `json:"keys"`
	State    contract.JobState  `json:"state"`
	Previous *contract.JobState `json:"previous"`
	Skipped  string             `json:"-"`
	internal string
}

var (
	testParams  = func() contract.Params { return &secretParams{} }
	testSubject = func(contract.Params) string { return "server" }
	testCommand = contract.Descriptor{Name: "test.secret", Kind: contract.KindJob, Audited: true,
		Params: testParams, Result: richResult{}, Subject: testSubject}
	e2eCommand = contract.Descriptor{Name: "e2e.test", Kind: contract.KindJob, Audited: true,
		Params: testParams, Result: richResult{}, Subject: testSubject, E2EOnly: true}
)

// decoded generates the schemas for descs and decodes each file.
func decoded(t *testing.T, descs []contract.Descriptor) map[string]map[string]any {
	t.Helper()
	files, err := generate(descs)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	out := map[string]map[string]any{}
	for name, data := range files {
		var doc map[string]any
		if err := json.Unmarshal(data, &doc); err != nil {
			t.Fatalf("%s is not JSON: %v", name, err)
		}
		out[name] = doc
	}
	return out
}

func readDir(t *testing.T, dir string) map[string][]byte {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string][]byte{}
	for _, e := range entries {
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		out[e.Name()] = data
	}
	return out
}

func TestSchemagenDeterministic(t *testing.T) {
	var runs []map[string][]byte
	for range 2 {
		dir := t.TempDir()
		if code := run([]string{"-out", dir}, io.Discard); code != 0 {
			t.Fatalf("run exited %d", code)
		}
		runs = append(runs, readDir(t, dir))
	}
	if !maps.EqualFunc(runs[0], runs[1], bytes.Equal) {
		t.Error("two runs wrote different files")
	}
	want := []string{"envelope.json"}
	for _, d := range contract.All() {
		want = append(want, d.Name+".params.json", d.Name+".result.json")
	}
	slices.Sort(want)
	if got := slices.Sorted(maps.Keys(runs[0])); !slices.Equal(got, want) {
		t.Errorf("files = %q, want %q", got, want)
	}
	for name, data := range runs[0] {
		if !bytes.HasSuffix(data, []byte("}\n")) {
			t.Errorf("%s doesn't end with a single newline", name)
		}
		var doc map[string]any
		if err := json.Unmarshal(data, &doc); err != nil {
			t.Fatalf("%s is not JSON: %v", name, err)
		}
		if doc["$schema"] != draft2020 {
			t.Errorf("%s: $schema = %v, want %s", name, doc["$schema"], draft2020)
		}
		// Re-encoding sorts every object's keys; sorted output comes back unchanged.
		var again bytes.Buffer
		enc := json.NewEncoder(&again)
		enc.SetEscapeHTML(false)
		enc.SetIndent("", "  ")
		if err := enc.Encode(doc); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(again.Bytes(), data) {
			t.Errorf("%s: keys aren't sorted, or the layout isn't canonical", name)
		}
	}
}

// walkSchema checks every object schema under node: a struct object (one with
// properties) must set additionalProperties to false, and a map object must
// give its values a schema. Unknown keywords fail, so nothing goes unchecked.
func walkSchema(t *testing.T, where string, node any) {
	t.Helper()
	s, ok := node.(map[string]any)
	if !ok {
		t.Errorf("%s: schema is %T, want an object", where, node)
		return
	}
	if s["type"] == "object" {
		ap, has := s["additionalProperties"]
		switch {
		case !has:
			t.Errorf("%s: object without additionalProperties", where)
		case s["properties"] != nil && ap != false:
			t.Errorf("%s: struct object has additionalProperties %v, want false", where, ap)
		case s["properties"] == nil && ap == false:
			t.Errorf("%s: object accepts nothing", where)
		}
	}
	for k, v := range s {
		at := where + "/" + k
		switch k {
		case "properties", "$defs":
			for name, sub := range v.(map[string]any) {
				walkSchema(t, at+"/"+name, sub)
			}
		case "items", "not":
			walkSchema(t, at, v)
		case "additionalProperties":
			if v != false {
				walkSchema(t, at, v)
			}
		case "anyOf", "oneOf":
			for i, sub := range v.([]any) {
				walkSchema(t, fmt.Sprintf("%s/%d", at, i), sub)
			}
		case "$schema", "$ref", "title", "type", "format", "pattern", "enum", "const",
			"minimum", "maximum", "maxLength", "maxItems", "required", "x-zimam-secret":
		default:
			t.Errorf("%s: unknown keyword", at)
		}
	}
}

func TestSchemasCloseObjects(t *testing.T) {
	docs := decoded(t, append(contract.All(), testCommand))
	for name, doc := range docs {
		walkSchema(t, name, doc)
	}
	// The test command's result holds a nested struct, a nullable value and a map.
	result := docs["test.secret.result.json"]
	props := result["properties"].(map[string]any)
	if got := props["labels"].(map[string]any)["additionalProperties"]; !reflect.DeepEqual(got, map[string]any{"type": "string"}) {
		t.Errorf("labels: additionalProperties = %v, want a string schema", got)
	}
	if got := props["maybe"].(map[string]any)["anyOf"]; got == nil {
		t.Error("maybe: a pointer isn't nullable")
	}
	required := result["required"].([]any)
	for _, name := range []string{"count", "when", "maybe", "keys"} {
		if !slices.Contains(required, any(name)) {
			t.Errorf("required lacks %s: %v", name, required)
		}
	}
	if slices.Contains(required, any("labels")) {
		t.Errorf("an omitempty field is required: %v", required)
	}
	for _, name := range []string{"Skipped", "-", "internal"} {
		if props[name] != nil {
			t.Errorf("an ignored field became property %q", name)
		}
	}
}

// checkSecrets walks Go type typ alongside its generated schema s. A property
// must carry "x-zimam-secret": true exactly when its field is tagged
// secret:"true". It returns how many secret fields it checked.
func checkSecrets(t *testing.T, path string, typ reflect.Type, s map[string]any) int {
	t.Helper()
	child := func(node any, key string) map[string]any {
		m, _ := node.(map[string]any)[key].(map[string]any)
		if m == nil {
			t.Errorf("%s: schema has no %q", path, key)
		}
		return m
	}
	switch typ.Kind() {
	case reflect.Pointer:
		branches, _ := s["anyOf"].([]any)
		if len(branches) != 2 {
			t.Errorf("%s: pointer schema isn't anyOf [value, null]", path)
			return 0
		}
		return checkSecrets(t, path, typ.Elem(), branches[0].(map[string]any))
	case reflect.Slice, reflect.Array:
		if typ.Elem().Kind() == reflect.Uint8 {
			return 0 // json.RawMessage and other bytes hold no fields
		}
		if items := child(s, "items"); items != nil {
			return checkSecrets(t, path+".*", typ.Elem(), items)
		}
	case reflect.Struct:
		if typ == reflect.TypeFor[time.Time]() {
			return 0
		}
		props := child(s, "properties")
		if props == nil {
			return 0
		}
		n := 0
		for i := range typ.NumField() {
			f := typ.Field(i)
			name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
			if !f.IsExported() || name == "-" {
				continue
			}
			p := child(props, name)
			if p == nil {
				continue
			}
			secret := f.Tag.Get("secret") == "true"
			if marked := p["x-zimam-secret"] == true; marked != secret {
				t.Errorf("%s.%s: x-zimam-secret = %v, want %v", path, name, marked, secret)
			}
			if secret {
				n++
			}
			n += checkSecrets(t, path+"."+name, f.Type, p)
		}
		return n
	}
	return 0
}

func TestSecretFieldsMarked(t *testing.T) {
	descs := append(contract.All(), testCommand)
	docs := decoded(t, descs)
	checked := 0
	for _, d := range descs {
		checked += checkSecrets(t, d.Name, reflect.TypeOf(d.Params()).Elem(), docs[d.Name+".params.json"])
		checked += checkSecrets(t, d.Name+" result", reflect.TypeOf(d.Result), docs[d.Name+".result.json"])
	}
	// test.secret: token, users.*.password_hash, keys.*.secret, backup.secret
	// in its params, and keys.*.secret in its result; site.security:
	// password_protection.users.*.password_hash.
	if checked != 6 {
		t.Errorf("checked %d secret fields, want 6", checked)
	}
}

func TestE2EOnlyNotGenerated(t *testing.T) {
	for _, d := range contract.All() {
		if d.E2EOnly {
			t.Errorf("%s is E2EOnly in a default build", d.Name)
		}
	}
	files, err := generate(append(contract.All(), e2eCommand))
	if err != nil {
		t.Fatal(err)
	}
	for name := range files {
		if strings.HasPrefix(name, "e2e.") {
			t.Errorf("generated %s for an E2EOnly command", name)
		}
	}
}

func TestEnvelopeSchema(t *testing.T) {
	env := decoded(t, contract.All())["envelope.json"]
	defs := env["$defs"].(map[string]any)
	if got := slices.Sorted(maps.Keys(defs)); !slices.Equal(got, []string{"accepted", "error", "log", "request", "result"}) {
		t.Errorf("$defs = %q", got)
	}
	var refs []string
	for _, r := range env["oneOf"].([]any) {
		refs = append(refs, r.(map[string]any)["$ref"].(string))
	}
	if want := []string{"#/$defs/accepted", "#/$defs/log", "#/$defs/result", "#/$defs/error"}; !slices.Equal(refs, want) {
		t.Errorf("oneOf = %q, want %q", refs, want)
	}
	for _, line := range []string{"accepted", "log", "result", "error"} {
		typ := defs[line].(map[string]any)["properties"].(map[string]any)["type"]
		if !reflect.DeepEqual(typ, map[string]any{"const": line}) {
			t.Errorf("%s: type = %v, want const %q", line, typ, line)
		}
	}
}

func TestGenerateRejectsUnsupportedFields(t *testing.T) {
	type untagged struct{ Name string }
	type embedded struct{ contract.BasicAuthUser }
	type channel struct {
		C chan int `json:"c"`
	}
	type intKeys struct {
		M map[int]string `json:"m"`
	}
	for name, result := range map[string]any{"untagged": untagged{}, "embedded": embedded{}, "chan": channel{}, "int keys": intKeys{}} {
		d := contract.Descriptor{Name: "test.bad", Kind: contract.KindRead, Params: testParams, Result: result}
		if _, err := generate([]contract.Descriptor{d}); err == nil {
			t.Errorf("%s: generate accepted it", name)
		}
	}
}

func TestWriteReplacesOnlyGeneratedFiles(t *testing.T) {
	dir := t.TempDir()
	for name, data := range map[string]string{
		"envelope.json":           "old",
		"gone.cmd.params.json":    "old",
		"gone.cmd.result.json":    "old",
		"notes.json":              "kept",
		"README.md":               "kept",
		"agent.version.data.json": "kept",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	files := map[string][]byte{"envelope.json": []byte("{}\n"), "job.status.params.json": []byte("{}\n")}
	if err := write(dir, files); err != nil {
		t.Fatalf("write: %v", err)
	}
	got := readDir(t, dir)
	want := map[string][]byte{
		"envelope.json":           []byte("{}\n"),
		"job.status.params.json":  []byte("{}\n"),
		"notes.json":              []byte("kept"),
		"README.md":               []byte("kept"),
		"agent.version.data.json": []byte("kept"),
	}
	if !maps.EqualFunc(got, want, bytes.Equal) {
		t.Errorf("dir holds %q, want %q", slices.Sorted(maps.Keys(got)), slices.Sorted(maps.Keys(want)))
	}
}
