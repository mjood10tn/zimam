package php

import (
	"slices"
	"testing"

	"zimam/agent/contract"
)

func TestPackagesPerVersion(t *testing.T) {
	every := []string{"fpm", "cli", "common", "mysql", "sqlite3", "mbstring", "xml", "curl", "zip", "gd", "intl", "bcmath", "readline"}
	for _, r := range contract.PHPAllowlist {
		v := string(r.Version)
		got := Packages(r.Version)
		want := []string{}
		for _, ext := range every {
			want = append(want, "php"+v+"-"+ext)
		}
		// §8.1: OPcache is a package of its own before 8.5; sury publishes
		// -redis for 8.2–8.5 (Task 1, 2026-10-27).
		if v != "8.5" {
			want = append(want, "php"+v+"-opcache")
		}
		want = append(want, "php"+v+"-redis")
		if !slices.Equal(got, want) {
			t.Errorf("Packages(%s) = %q, want %q", v, got, want)
		}
	}
}
