package audit

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"zimam/agent/contract"
)

type loginParams struct {
	Name     string `json:"name"`
	Password string `json:"password" secret:"true"`
	Skipped  string `json:"-"`
	internal string
	Count    int
}

func TestRedactTopLevelSecret(t *testing.T) {
	p := loginParams{Name: "shop", Password: "hunter2", Skipped: "x", internal: "y", Count: 3}
	want := map[string]any{"name": "shop", "password": "[redacted]", "Count": 3}
	if got := Redact(p); !reflect.DeepEqual(got, want) {
		t.Errorf("Redact(value) = %#v, want %#v", got, want)
	}
	if got := Redact(&p); !reflect.DeepEqual(got, want) {
		t.Errorf("Redact(pointer) = %#v, want %#v", got, want)
	}
}

func TestRedactNestedSliceSecret(t *testing.T) {
	type basicAuthParams struct {
		Site  contract.SiteName       `json:"site"`
		Users contract.BasicAuthUsers `json:"users"`
	}
	hash := "$2y$10$abcdefghijklmnopqrstuuABCDEFGHIJKLMNOPQRSTUVWXYZ01234"
	p := basicAuthParams{
		Site:  "shop",
		Users: contract.BasicAuthUsers{{Username: "ann", PasswordHash: hash}, {Username: "bob", PasswordHash: hash}},
	}
	want := map[string]any{
		"site": contract.SiteName("shop"),
		"users": []any{
			map[string]any{"username": "ann", "password_hash": "[redacted]"},
			map[string]any{"username": "bob", "password_hash": "[redacted]"},
		},
	}
	got := Redact(p)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Redact = %#v, want %#v", got, want)
	}
	out, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), hash) {
		t.Errorf("redacted JSON still holds the hash: %s", out)
	}
}

func TestRedactNil(t *testing.T) {
	if got := Redact(nil); got != nil {
		t.Errorf("Redact(nil) = %#v, want nil", got)
	}
	if got := Redact((*loginParams)(nil)); got != nil {
		t.Errorf("Redact(nil pointer) = %#v, want nil", got)
	}
	type withNils struct {
		Users contract.BasicAuthUsers `json:"users"`
		Extra any                     `json:"extra"`
		Ptr   *int                    `json:"ptr"`
	}
	want := map[string]any{"users": nil, "extra": nil, "ptr": nil}
	if got := Redact(withNils{}); !reflect.DeepEqual(got, want) {
		t.Errorf("Redact(nil fields) = %#v, want %#v", got, want)
	}
}

func TestRedactMapIsRedacted(t *testing.T) {
	type withMap struct {
		Env map[string]string `json:"env"`
	}
	p := withMap{Env: map[string]string{"DB_PASSWORD": "hunter2"}}
	want := map[string]any{"env": "[redacted]"}
	if got := Redact(p); !reflect.DeepEqual(got, want) {
		t.Errorf("Redact(map field) = %#v, want %#v", got, want)
	}
	if got := Redact(map[string]any{"password": "hunter2"}); got != "[redacted]" {
		t.Errorf("Redact(map) = %#v, want \"[redacted]\"", got)
	}
}
