package contract

import (
	"strings"
	"testing"
)

const validRequest = `{"v":1,"id":"01JBX7Q2M5YQ8T3N6V0R4K9W1E","command":"site.provision","actor":"user:1","params":{"name":"shop","domain":"shop.example.com","type":"php","php_version":"8.4"}}`

func TestDecodeStrict(t *testing.T) {
	var req Request
	if err := DecodeStrict([]byte(validRequest), &req); err != nil {
		t.Fatalf("valid request: %v", err)
	}
	if req.V != 1 || req.Command != "site.provision" || req.Actor != "user:1" || !strings.Contains(string(req.Params), `"php_version":"8.4"`) {
		t.Errorf("decoded %+v", req)
	}

	for name, in := range map[string]string{
		"unknown field":            `{"v":1,"id":"x","command":"c","actor":"a","params":{},"extra":1}`,
		"uppercase key":            `{"V":1}`,
		"camelCase key":            `{"v":1,"phpVersion":"8.4"}`,
		"duplicate key":            `{"v":1,"v":2}`,
		"duplicate key in params":  `{"v":1,"id":"x","command":"c","actor":"a","params":{"name":"a","name":"b"}}`,
		"uppercase key in params":  `{"v":1,"id":"x","command":"c","actor":"a","params":{"Name":"a"}}`,
		"duplicate in nested list": `{"v":1,"params":{"users":[{"username":"a","username":"b"}]}}`,
		"two values":               `{} {}`,
		"trailing garbage":         `{"v":1} x`,
		"array":                    `[]`,
		"string":                   `"x"`,
		"number":                   `1`,
		"null":                     `null`,
		"invalid UTF-8 in a key":   "{\"v\xff\":1}",
		"invalid UTF-8 in a value": "{\"v\":1,\"id\":\"x\",\"command\":\"c\",\"actor\":\"a\xff\",\"params\":{}}",
		"empty":                    ``,
		"truncated":                `{"v":1`,
	} {
		var req Request
		if err := DecodeStrict([]byte(in), &req); err == nil {
			t.Errorf("%s: DecodeStrict(%q) = nil, want an error", name, in)
		}
	}
}

func TestDecodeStrictParams(t *testing.T) {
	type params struct {
		Name SiteName `json:"name"`
	}
	var p params
	if err := DecodeStrict([]byte(`{"name":"shop"}`), &p); err != nil || p.Name != "shop" {
		t.Fatalf("got %+v, %v", p, err)
	}
	for _, in := range []string{`{"name":["shop"]}`, `{"name":"a","name":"b"}`, `{"nAme":"shop"}`, `null`} {
		if err := DecodeStrict([]byte(in), &p); err == nil {
			t.Errorf("DecodeStrict(%q) = nil, want an error", in)
		}
	}
}
