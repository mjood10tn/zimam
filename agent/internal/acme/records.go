package acme

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"zimam/agent/contract"
	"zimam/agent/internal/configtx"
	"zimam/agent/internal/sysexec"
)

// Record is /var/lib/zimam/certs/<name>.json, a certificate's expiry
// (§10). security.check reads it for certificate_valid (§9.7).
type Record struct {
	Name   string    `json:"name"`   // "panel" or the site's name
	Expiry time.Time `json:"expiry"` // RFC 3339, UTC
}

// recordName is the record name for a certificate name: "panel" for
// zimam-panel, the site's name for zimam-<site>.
func recordName(certName string) (string, error) {
	name, ok := strings.CutPrefix(certName, "zimam-")
	if !ok || checkName(name) != nil {
		return "", fmt.Errorf("certificate name %q is neither zimam-panel nor zimam-<site>", certName)
	}
	return name, nil
}

// checkName accepts "panel" and valid site names, which can't be "panel".
func checkName(name string) error {
	if name == "panel" {
		return nil
	}
	if err := contract.SiteName(name).Validate(); err != nil {
		return fmt.Errorf("%q is neither the panel nor a site name", name)
	}
	return nil
}

// WriteRecord writes a certificate's expiry record, root 0600, in the
// certs directory (0700).
func WriteRecord(d sysexec.Deps, name string, expiry time.Time) error {
	if err := checkName(name); err != nil {
		return err
	}
	data, err := json.MarshalIndent(Record{Name: name, Expiry: expiry.UTC()}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(d.Paths.CertsDir(), 0o700); err != nil {
		return err
	}
	noTest := func(context.Context) error { return nil }
	return configtx.Apply(context.Background(), []configtx.Change{
		configtx.RootFile(d.Paths.CertRecord(name), append(data, '\n'), 0o600),
	}, noTest)
}
