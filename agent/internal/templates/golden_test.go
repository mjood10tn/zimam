package templates

import (
	"bytes"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"zimam/agent/contract"
)

var update = flag.Bool("update", false, "rewrite testdata/golden from the templates")

const goldenDir = "testdata/golden"

// goldenFile is one rendered file of a golden case, at its path on a server.
type goldenFile struct {
	path string
	tmpl string
	data func() any // nil for a template that takes no data
}

func (f goldenFile) render() ([]byte, error) {
	var data any
	if f.data != nil {
		data = f.data()
	}
	return Render(f.tmpl, data)
}

// goldenCases are the file trees configcheck installs and tests, each in a
// fresh container (design §14.1). A case must work on its own, so a vhost
// brings the snippets it includes.
var goldenCases = map[string][]goldenFile{
	// What bootstrap writes for Nginx (§4 step 5).
	"nginx-base": {
		{path: "/etc/nginx/zimam/tls.conf", tmpl: "nginx/tls.conf"},
		{path: "/etc/nginx/sites-available/zimam-default.conf", tmpl: "nginx/default.conf"},
	},
}

func TestGolden(t *testing.T) {
	if *update {
		if err := os.RemoveAll(goldenDir); err != nil {
			t.Fatal(err)
		}
	}
	for name, files := range goldenCases {
		for _, f := range files {
			if !path.IsAbs(f.path) || path.Clean(f.path) != f.path {
				t.Errorf("%s: %q isn't a clean absolute path", name, f.path)
				continue
			}
			out, err := f.render()
			if err != nil {
				t.Errorf("%s %s: %v", name, f.path, err)
				continue
			}
			golden := filepath.Join(goldenDir, name, filepath.FromSlash(f.path))
			if *update {
				if err := os.MkdirAll(filepath.Dir(golden), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(golden, out, 0o644); err != nil {
					t.Fatal(err)
				}
				continue
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Errorf("%s %s: %v (go test ./internal/templates -update writes it)", name, f.path, err)
				continue
			}
			if !bytes.Equal(out, want) {
				t.Errorf("%s %s differs from %s:\n--- rendered\n%s--- golden\n%s", name, f.path, golden, out, want)
			}
		}
	}
	// configcheck tests every file under testdata/golden, so none may be left
	// over from a case or file that is gone.
	err := filepath.WalkDir(goldenDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, err := filepath.Rel(goldenDir, p)
		if err != nil {
			return err
		}
		name, file, _ := strings.Cut(filepath.ToSlash(rel), "/")
		if !slices.ContainsFunc(goldenCases[name], func(f goldenFile) bool { return f.path == "/"+file }) {
			t.Errorf("%s belongs to no golden case (go test ./internal/templates -update removes it)", p)
		}
		return nil
	})
	if err != nil {
		t.Error(err)
	}
}

// stringsIn returns an addressable copy of data and every settable string in
// it: struct fields, slice and array elements, and what pointers point at.
func stringsIn(data any) (reflect.Value, []reflect.Value) {
	root := reflect.New(reflect.TypeOf(data)).Elem()
	root.Set(reflect.ValueOf(data))
	var found []reflect.Value
	var walk func(v reflect.Value)
	walk = func(v reflect.Value) {
		switch v.Kind() {
		case reflect.String:
			if v.CanSet() {
				found = append(found, v)
			}
		case reflect.Struct:
			for i := range v.NumField() {
				walk(v.Field(i))
			}
		case reflect.Slice, reflect.Array:
			for i := range v.Len() {
				walk(v.Index(i))
			}
		case reflect.Pointer:
			if !v.IsNil() {
				walk(v.Elem())
			}
		}
	}
	walk(root)
	return root, found
}

func TestStringsInFindsNestedStrings(t *testing.T) {
	type inner struct {
		Name contract.SiteName
		Tags []string
	}
	data := struct {
		Domain contract.Domain
		Port   int
		Inner  *inner
		Users  []contract.BasicAuthUser
		hidden string
	}{
		Inner: &inner{Tags: []string{"a", "b"}},
		Users: []contract.BasicAuthUser{{}},
	}
	// Domain, Inner.Name, the two tags, Username and PasswordHash.
	if _, found := stringsIn(data); len(found) != 6 {
		t.Errorf("found %d strings, want 6", len(found))
	}
}

// FuzzRenderSafe puts random strings through safe, and into every string a
// golden case's data holds. Rendering either fails, or the value appears
// exactly as given and adds no line break.
func FuzzRenderSafe(f *testing.F) {
	for _, s := range []string{"shop.example.com", "packages.sury.org", "a;b", "a\nb", "a{b", "$host", `a"b`, "a b", ""} {
		f.Add(s)
	}
	set := testSet(f, map[string]string{"test/safe.conf": "server_name {{safe .}};\n"})
	f.Fuzz(func(t *testing.T, s string) {
		check := func(what string, out []byte, err error) {
			if err != nil {
				return
			}
			if strings.ContainsAny(s, "\r\n") {
				t.Errorf("%s rendered a value with a line break: %q", what, s)
			}
			if !bytes.Contains(out, []byte(s)) {
				t.Errorf("%s didn't render %q as given:\n%s", what, s, out)
			}
		}
		out, err := render(set, "test/safe.conf", s)
		check("safe", out, err)
		for name, files := range goldenCases {
			for _, gf := range files {
				if gf.data == nil {
					continue
				}
				_, fields := stringsIn(gf.data())
				for i := range fields {
					root, fields := stringsIn(gf.data())
					fields[i].SetString(s)
					out, err := Render(gf.tmpl, root.Interface())
					check(fmt.Sprintf("%s %s (string %d)", name, gf.tmpl, i), out, err)
				}
			}
		}
	})
}
