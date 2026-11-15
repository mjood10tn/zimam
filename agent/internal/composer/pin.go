package composer

// The pinned Composer (R02-D6, spike amendment A9): checked on 2026-11-15
// against getcomposer.org/download/2.11.2/composer.phar.sha256sum and the
// downloaded file, and run on PHP 8.2 to 8.5. Each Zimam release may move
// it.
const (
	Version = "2.11.2"
	SHA256  = "dd158c0afd41d15fac89e1b4e47b5d42911fe316c3687cd5edac98319b25d028"
)

// pin is one Composer release: its version, the phar's SHA-256 and where it
// is downloaded from.
type pin struct {
	version string
	sha256  string // lowercase hex
	url     string
}

var pinned = pin{
	version: Version,
	sha256:  SHA256,
	url:     "https://getcomposer.org/download/" + Version + "/composer.phar",
}
