package contract

import (
	"testing"
	"time"
)

func TestNodeMajor(t *testing.T) {
	checkValues(t, []NodeMajor{"24", "26"}, []NodeMajor{"", "22", "25", "27", "26.1", "26\n", "latest", "v26"})
	if DefaultNodeMajor != "26" {
		t.Errorf("default major %q, want 26", DefaultNodeMajor)
	}
}

func TestNodeSupportedOn(t *testing.T) {
	r, ok := NodeMajor("24").Release()
	if !ok || r.EndOfLife != "2028-04-30" {
		t.Fatalf("24 = %+v, %v; want end of life 2028-04-30", r, ok)
	}
	if !r.SupportedOn(time.Date(2028, 4, 30, 23, 59, 0, 0, time.UTC)) || r.SupportedOn(time.Date(2028, 5, 1, 0, 0, 0, 0, time.UTC)) {
		t.Error("24 should be supported through 2028-04-30 only")
	}
	if r, ok := NodeMajor("26").Release(); !ok || r.EndOfLife != "2029-04-30" {
		t.Errorf("26 = %+v, %v; want end of life 2029-04-30", r, ok)
	}
}

func TestNodeInstallParams(t *testing.T) {
	if err := (&NodeInstallParams{}).Validate(); err != nil {
		t.Errorf("no major: %v", err)
	}
	if (NodeInstallParams{}).Chosen() != DefaultNodeMajor || (NodeInstallParams{Major: "24"}).Chosen() != "24" {
		t.Error("Chosen does not apply the default")
	}
	if fields(t, &NodeInstallParams{Major: "22"})["major"] == "" {
		t.Error("accepted major 22")
	}
}
