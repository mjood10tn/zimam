// Package pin is the pinned Composer release (R02-D6, spike amendment A9),
// apart from the download code, so the helper can name the phar's path
// without linking an HTTP client.
package pin

// Checked on 2026-11-15 against
// getcomposer.org/download/2.11.2/composer.phar.sha256sum and the
// downloaded file, and run on PHP 8.2 to 8.5. Each Zimam release may move
// it.
const (
	Version = "2.11.2"
	SHA256  = "dd158c0afd41d15fac89e1b4e47b5d42911fe316c3687cd5edac98319b25d028"
)
