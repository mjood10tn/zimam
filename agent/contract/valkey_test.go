package contract

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestDestroyDropDatabasesDefaultsFalse(t *testing.T) {
	d, ok := Lookup("site.destroy")
	if !ok {
		t.Fatal("site.destroy isn't registered")
	}
	p := d.Params()
	if err := DecodeStrict([]byte(`{"name":"shop"}`), p); err != nil {
		t.Fatal(err)
	}
	got, ok := p.(*SiteDestroyParams)
	if !ok {
		t.Fatalf("site.destroy params are %T, want *SiteDestroyParams", p)
	}
	if got.DropDatabases {
		t.Error("drop_databases is true when left out")
	}
	if f := fields(t, got); f != nil {
		t.Errorf("a plain destroy: %v", f)
	}
	if b, _ := json.Marshal(SiteDestroyParams{Name: "shop"}); strings.Contains(string(b), "drop_databases") {
		t.Errorf("false is encoded: %s", b)
	}
	p = d.Params()
	if err := DecodeStrict([]byte(`{"name":"shop","drop_databases":true}`), p); err != nil || !p.(*SiteDestroyParams).DropDatabases {
		t.Errorf("drop_databases true: %v, %+v", err, p)
	}
	wantRefused(t, &SiteDestroyParams{Name: "Shop"}, "name")
	if got := d.Subject(&SiteDestroyParams{Name: "shop"}); got != "site:shop" {
		t.Errorf("subject %q", got)
	}
}

// system.status's services are additive: a result without them still
// decodes, and an unset service reports not installed.
func TestSystemStatusServices(t *testing.T) {
	var r SystemStatusResult
	if err := json.Unmarshal([]byte(`{"agent_version":"1"}`), &r); err != nil {
		t.Fatal(err)
	}
	r.Services = ServicesStatus{
		MySQL:  &ServiceStatus{Installed: true, Version: "8.4.13", Active: true},
		Valkey: &ServiceStatus{},
	}
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	want := `"services":{"mysql":{"installed":true,"version":"8.4.13","active":true},"valkey":{"installed":false,"version":"","active":false}}`
	if !strings.Contains(string(b), want) {
		t.Errorf("encoded %s, want it to contain %s", b, want)
	}
}
