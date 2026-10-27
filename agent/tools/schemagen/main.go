// Command schemagen writes the JSON Schemas of the agent protocol (design §6):
// envelope.json, and <command>.params.json and <command>.result.json for
// every registered command except E2EOnly ones. It runs through go generate
// in agent/contract, and uses only the standard library (D14).
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"time"

	"zimam/agent/contract"
)

const draft2020 = "https://json-schema.org/draft/2020-12/schema"

func main() {
	os.Exit(run(os.Args[1:], os.Stderr))
}

func run(args []string, stderr io.Writer) int {
	fs := flag.NewFlagSet("schemagen", flag.ContinueOnError)
	fs.SetOutput(stderr)
	out := fs.String("out", "", "directory to write the schemas into")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *out == "" || fs.NArg() != 0 {
		fmt.Fprintln(stderr, "usage: schemagen -out <dir>")
		return 2
	}
	files, err := generate(contract.All())
	if err == nil {
		err = write(*out, files)
	}
	if err != nil {
		fmt.Fprintln(stderr, "schemagen:", err)
		return 1
	}
	return 0
}

// generate returns the schema files for descs by file name, each encoded
// with sorted keys and one trailing newline. E2EOnly commands are skipped.
func generate(descs []contract.Descriptor) (map[string][]byte, error) {
	docs := map[string]map[string]any{}
	env, err := envelope()
	if err != nil {
		return nil, err
	}
	docs["envelope.json"] = env
	for _, d := range descs {
		if d.E2EOnly {
			continue
		}
		if d.Params == nil || d.Result == nil || strings.ContainsAny(d.Name, `/\`) {
			return nil, fmt.Errorf("%q: malformed descriptor", d.Name)
		}
		params := reflect.TypeOf(d.Params())
		if params.Kind() == reflect.Pointer {
			params = params.Elem()
		}
		for part, t := range map[string]reflect.Type{"params": params, "result": reflect.TypeOf(d.Result)} {
			s, err := newGen().schema(t)
			if err != nil {
				return nil, fmt.Errorf("%s %s: %w", d.Name, part, err)
			}
			s["$schema"] = draft2020
			s["title"] = d.Name + " " + part
			name := d.Name + "." + part + ".json"
			if docs[name] != nil {
				return nil, fmt.Errorf("%s: registered twice", d.Name)
			}
			docs[name] = s
		}
	}
	files := map[string][]byte{}
	for name, doc := range docs {
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.SetEscapeHTML(false)
		enc.SetIndent("", "  ")
		if err := enc.Encode(doc); err != nil { // sorts map keys, ends with "\n"
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		files[name] = buf.Bytes()
	}
	return files, nil
}

// envelope describes one response line (the top-level oneOf, told apart by
// its "type"), with the request line under $defs/request.
func envelope() (map[string]any, error) {
	defs := map[string]any{}
	lines := []struct {
		name string
		v    any
	}{
		{"request", contract.Request{}},
		{"accepted", contract.Accepted{}},
		{"log", contract.LogLine{}},
		{"result", contract.Result{}},
		{"error", contract.Error{}},
	}
	var oneOf []any
	for _, l := range lines {
		s, err := newGen().schema(reflect.TypeOf(l.v))
		if err != nil {
			return nil, fmt.Errorf("envelope %s: %w", l.name, err)
		}
		if l.name != "request" {
			s["properties"].(map[string]any)["type"] = map[string]any{"const": l.name}
			oneOf = append(oneOf, map[string]any{"$ref": "#/$defs/" + l.name})
		}
		defs[l.name] = s
	}
	return map[string]any{
		"$schema": draft2020,
		"title":   "agent response line (the request line is $defs/request)",
		"$defs":   defs,
		"oneOf":   oneOf,
	}, nil
}

type schemer interface{ JSONSchema() contract.Schema }

var (
	timeType = reflect.TypeFor[time.Time]()
	rawType  = reflect.TypeFor[json.RawMessage]()
)

// gen builds the schema of one Go type. inProgress catches types that
// contain themselves, which inline schemas can't express.
type gen struct{ inProgress map[reflect.Type]bool }

func newGen() *gen { return &gen{inProgress: map[reflect.Type]bool{}} }

func (g *gen) schema(t reflect.Type) (map[string]any, error) {
	switch {
	case t == rawType:
		return map[string]any{}, nil // any JSON value
	case t == timeType:
		return map[string]any{"type": "string", "format": "date-time"}, nil
	case t.Kind() != reflect.Interface && t.Kind() != reflect.Pointer:
		if s, ok := reflect.New(t).Interface().(schemer); ok {
			return ownCopy(s.JSONSchema())
		}
	}
	switch t.Kind() {
	case reflect.Bool:
		return map[string]any{"type": "boolean"}, nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return map[string]any{"type": "integer"}, nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return map[string]any{"type": "integer", "minimum": 0}, nil
	case reflect.Float32, reflect.Float64:
		return map[string]any{"type": "number"}, nil
	case reflect.String:
		return map[string]any{"type": "string"}, nil
	case reflect.Interface:
		return map[string]any{}, nil
	case reflect.Pointer:
		inner, err := g.schema(t.Elem())
		if err != nil {
			return nil, err
		}
		return map[string]any{"anyOf": []any{inner, map[string]any{"type": "null"}}}, nil
	case reflect.Slice:
		if t.Elem().Kind() == reflect.Uint8 {
			return nil, fmt.Errorf("%s: byte slices aren't supported", t)
		}
		items, err := g.schema(t.Elem())
		if err != nil {
			return nil, err
		}
		return map[string]any{"type": "array", "items": items}, nil
	case reflect.Map:
		if t.Key().Kind() != reflect.String {
			return nil, fmt.Errorf("%s: map keys must be strings", t)
		}
		values, err := g.schema(t.Elem())
		if err != nil {
			return nil, err
		}
		return map[string]any{"type": "object", "additionalProperties": values}, nil
	case reflect.Struct:
		return g.object(t)
	}
	return nil, fmt.Errorf("%s: %s values aren't supported", t, t.Kind())
}

// object is a closed object: every exported field is a property under its
// json name, and the fields without omitempty or omitzero are required.
func (g *gen) object(t reflect.Type) (map[string]any, error) {
	if g.inProgress[t] {
		return nil, fmt.Errorf("%s contains itself", t)
	}
	g.inProgress[t] = true
	defer delete(g.inProgress, t)

	props := map[string]any{}
	required := []string{}
	for i := range t.NumField() {
		f := t.Field(i)
		if f.Anonymous {
			return nil, fmt.Errorf("%s.%s: embedded fields aren't supported", t, f.Name)
		}
		tag := f.Tag.Get("json")
		if !f.IsExported() || tag == "-" {
			continue
		}
		name, opts, _ := strings.Cut(tag, ",")
		if name == "" {
			return nil, fmt.Errorf("%s.%s: needs a json tag with a name", t, f.Name)
		}
		if props[name] != nil {
			return nil, fmt.Errorf("%s: two fields are named %q", t, name)
		}
		options := strings.Split(opts, ",")
		if slices.Contains(options, "string") {
			return nil, fmt.Errorf("%s.%s: the json string option isn't supported", t, f.Name)
		}
		s, err := g.schema(f.Type)
		if err != nil {
			return nil, fmt.Errorf("%s.%s: %w", t, f.Name, err)
		}
		if f.Tag.Get("secret") == "true" {
			s["x-zimam-secret"] = true // outermost, even around anyOf
		}
		props[name] = s
		if !slices.Contains(options, "omitempty") && !slices.Contains(options, "omitzero") {
			required = append(required, name)
		}
	}
	s := map[string]any{"type": "object", "properties": props, "additionalProperties": false}
	if len(required) > 0 {
		slices.Sort(required)
		s["required"] = required
	}
	return s, nil
}

// ownCopy turns a named type's Schema into plain JSON values, so nothing the
// generator adds can reach a map the type shares.
func ownCopy(s contract.Schema) (map[string]any, error) {
	b, err := json.Marshal(s)
	if err != nil {
		return nil, err
	}
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var out map[string]any
	if err := dec.Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}

// write puts files into dir and deletes the schema files it no longer
// generates. Other files in dir are left alone.
func write(dir string, files map[string][]byte) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if generated(e.Name()) && files[e.Name()] == nil {
			if err := os.Remove(filepath.Join(dir, e.Name())); err != nil {
				return err
			}
		}
	}
	for _, name := range slices.Sorted(maps.Keys(files)) {
		if err := os.WriteFile(filepath.Join(dir, name), files[name], 0o644); err != nil {
			return err
		}
	}
	return nil
}

// generated reports whether name is a file schemagen writes.
func generated(name string) bool {
	return name == "envelope.json" || strings.HasSuffix(name, ".params.json") || strings.HasSuffix(name, ".result.json")
}
