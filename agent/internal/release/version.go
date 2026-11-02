// Package release holds what a build stamps into the agent binary, and
// checks a signed release manifest (design §3).
package release

import (
	"cmp"
	"regexp"
	"strings"
)

// Set at build time by .github/scripts/build-release.sh with
// -ldflags "-X zimam/agent/internal/release.<Name>=<value>". -X only acts on
// variables, so these must stay vars. Development builds keep the defaults.
var (
	// Version is the release's SemVer version.
	Version = "0.0.0-dev"
	// PublicKey is the base64 of the raw 32-byte Ed25519 release key.
	PublicKey = ""
	// BaseURL is where the release's artifacts are downloaded from.
	BaseURL = ""
)

// semver is SemVer 2.0.0's own pattern (semver.org): major, minor, patch,
// then an optional pre-release and build metadata.
var semver = regexp.MustCompile(`^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)` +
	`(?:-((?:0|[1-9]\d*|\d*[a-zA-Z-][0-9a-zA-Z-]*)(?:\.(?:0|[1-9]\d*|\d*[a-zA-Z-][0-9a-zA-Z-]*))*))?` +
	`(?:\+([0-9a-zA-Z-]+(?:\.[0-9a-zA-Z-]+)*))?$`)

// ValidVersion reports whether v is a SemVer 2.0.0 version, with no "v".
func ValidVersion(v string) bool {
	return semver.MatchString(v)
}

// CompareVersions returns -1, 0 or +1 as a is lower than, equal to or
// higher than b in SemVer precedence. Build metadata is ignored. A string
// that isn't SemVer sorts before every version, so it never meets a minimum.
func CompareVersions(a, b string) int {
	va, vb := semver.FindStringSubmatch(a), semver.FindStringSubmatch(b)
	switch {
	case va == nil && vb == nil:
		return 0
	case va == nil:
		return -1
	case vb == nil:
		return 1
	}
	for i := 1; i <= 3; i++ {
		if c := compareNumbers(va[i], vb[i]); c != 0 {
			return c
		}
	}
	return comparePrereleases(va[4], vb[4])
}

// compareNumbers compares two decimal numbers without leading zeros, of any
// length.
func compareNumbers(a, b string) int {
	if c := cmp.Compare(len(a), len(b)); c != 0 {
		return c
	}
	return strings.Compare(a, b)
}

// comparePrereleases compares dot-separated pre-release identifiers. No
// pre-release ranks above any pre-release, numeric identifiers rank below
// alphanumeric ones, and a shorter list ranks below a longer one it begins.
func comparePrereleases(a, b string) int {
	switch {
	case a == b:
		return 0
	case a == "":
		return 1
	case b == "":
		return -1
	}
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := range min(len(as), len(bs)) {
		x, y := as[i], bs[i]
		xn, yn := isNumber(x), isNumber(y)
		var c int
		switch {
		case xn && yn:
			c = compareNumbers(x, y)
		case xn:
			c = -1
		case yn:
			c = 1
		default:
			c = strings.Compare(x, y)
		}
		if c != 0 {
			return c
		}
	}
	return cmp.Compare(len(as), len(bs))
}

func isNumber(s string) bool {
	return strings.Trim(s, "0123456789") == ""
}
