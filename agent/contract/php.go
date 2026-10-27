package contract

import (
	"errors"
	"time"
)

type PHPRelease struct {
	Version     PHPVersion
	SecurityEnd string // last day of security support (php.net), YYYY-MM-DD
}

// PHPAllowlist is §8.1. 8.6 joins in the first agent release after PHP 8.6 is stable.
var PHPAllowlist = []PHPRelease{
	{"8.2", "2026-12-31"},
	{"8.3", "2027-12-31"},
	{"8.4", "2028-12-31"},
	{"8.5", "2029-12-31"},
}

type PHPVersion string

func (v PHPVersion) Release() (PHPRelease, bool) {
	for _, r := range PHPAllowlist {
		if r.Version == v {
			return r, true
		}
	}
	return PHPRelease{}, false
}

func (v PHPVersion) Validate() error {
	if _, ok := v.Release(); !ok {
		return errors.New("is not a supported PHP version")
	}
	return nil
}

func (PHPVersion) JSONSchema() Schema {
	versions := make([]PHPVersion, len(PHPAllowlist))
	for i, r := range PHPAllowlist {
		versions[i] = r.Version
	}
	return Schema{"type": "string", "enum": versions}
}

// SupportedOn reports whether r can still be installed or chosen for a new
// site on the given day: through its end date, not after it.
func (r PHPRelease) SupportedOn(now time.Time) bool {
	end, _ := time.Parse(time.DateOnly, r.SecurityEnd)
	return now.UTC().Before(end.AddDate(0, 0, 1))
}
