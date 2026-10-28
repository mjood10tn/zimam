// Package templates renders every file root reads (design §5.5) from
// templates embedded in the agent, filled only with validated values.
package templates

import (
	"bytes"
	"embed"
	"fmt"
	"io/fs"
	"strings"
	"text/template"
)

//go:embed files
var embedded embed.FS

// set holds every template under files/, named by its path there without
// ".tmpl", such as "nginx/tls.conf".
var set = func() *template.Template {
	files, err := fs.Sub(embedded, "files")
	if err == nil {
		var s *template.Template
		if s, err = parse(files); err == nil {
			return s
		}
	}
	panic(err)
}()

// parse reads every *.tmpl file in fsys into one set, so a template can
// include another. Executing with a missing map key is an error.
func parse(fsys fs.FS) (*template.Template, error) {
	set := template.New("").Option("missingkey=error").Funcs(funcs)
	err := fs.WalkDir(fsys, ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, ".tmpl") {
			return err
		}
		body, err := fs.ReadFile(fsys, path)
		if err != nil {
			return err
		}
		_, err = set.New(strings.TrimSuffix(path, ".tmpl")).Parse(string(body))
		return err
	})
	if err != nil {
		return nil, err
	}
	return set, nil
}

// Render executes the template name, such as "nginx/tls.conf", with data.
func Render(name string, data any) ([]byte, error) { return render(set, name, data) }

func render(set *template.Template, name string, data any) ([]byte, error) {
	t := set.Lookup(name)
	if t == nil || name == "" {
		return nil, fmt.Errorf("templates: no template %q", name)
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
