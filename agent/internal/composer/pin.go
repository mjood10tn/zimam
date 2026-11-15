package composer

import "zimam/agent/internal/composer/pin"

// release is one Composer release: its version, the phar's SHA-256 and
// where it is downloaded from.
type release struct {
	version string
	sha256  string // lowercase hex
	url     string
}

// pinned is the release the agent uses (package pin).
var pinned = release{
	version: pin.Version,
	sha256:  pin.SHA256,
	url:     "https://getcomposer.org/download/" + pin.Version + "/composer.phar",
}
