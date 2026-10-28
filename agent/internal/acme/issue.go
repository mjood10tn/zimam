// Package acme issues certificates with certbot over HTTP-01 through the
// shared webroot (design §10), and keeps their expiry records.
package acme

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"zimam/agent/contract"
	"zimam/agent/internal/sysexec"
)

const (
	certbot = "/usr/bin/certbot"
	// failureLines is how much of certbot's output a failed job shows.
	failureLines = 20
)

// Request is one certificate to issue.
type Request struct {
	CertName string // zimam-panel, or zimam-<site>
	Domain   contract.Domain
	DryRun   bool // certonly --dry-run: surfaces DNS and firewall problems, saves nothing
}

// Issue checks that the domain resolves, then runs certbot certonly with
// §10's arguments, and returns the new certificate's expiry (the zero time
// for a dry run). After a real issuance certbot runs the deploy hook,
// zimam-agent hook cert-deployed, which takes the reload lock: never call
// Issue while holding it.
func Issue(ctx context.Context, d sysexec.Deps, r Request) (time.Time, error) {
	if _, err := recordName(r.CertName); err != nil {
		return time.Time{}, err
	}
	if err := r.Domain.Validate(); err != nil {
		return time.Time{}, fmt.Errorf("domain %q %v", r.Domain, err)
	}
	domain := string(r.Domain)
	addrs, err := d.DNSResolver().LookupIPAddr(ctx, domain)
	if err != nil {
		return time.Time{}, fmt.Errorf("%s doesn't resolve: %w", domain, err)
	}
	if len(addrs) == 0 {
		return time.Time{}, fmt.Errorf("%s has no A or AAAA record", domain)
	}
	for _, a := range addrs {
		if a.IP.To4() == nil {
			d.Logf("%s has an AAAA record, so Let's Encrypt may validate it over IPv6: port 80 must answer there too", domain)
			break
		}
	}
	args := []string{"certonly", "--webroot", "-w", d.Paths.ACMEWebroot(), "--cert-name", r.CertName, "-d", domain,
		"--non-interactive", "--agree-tos"}
	if d.Config.ACMEEmail != "" {
		args = append(args, "--email", d.Config.ACMEEmail)
	} else {
		args = append(args, "--register-unsafely-without-email")
	}
	args = append(args, "--server", d.Config.ACMEDirectory, "--deploy-hook", d.Paths.AgentBinary()+" hook cert-deployed")
	if r.DryRun {
		args = append(args, "--dry-run")
	}
	var env []string
	if d.Config.ACMECABundle != "" {
		env = append(env, "REQUESTS_CA_BUNDLE="+d.Config.ACMECABundle)
	}
	if r.DryRun {
		d.Logf("Trying a certificate for %s without saving it (certbot --dry-run)", domain)
	} else {
		d.Logf("Requesting a certificate for %s", domain)
	}
	res, err := d.Run.Run(ctx, sysexec.Cmd{Path: certbot, Args: args, Env: env})
	if errors.As(err, new(*sysexec.ExitError)) {
		return time.Time{}, fmt.Errorf("certbot couldn't get a certificate for %s:\n%s", domain, lastLines(string(res.Stdout)+string(res.Stderr), failureLines))
	}
	if err != nil {
		return time.Time{}, err
	}
	if r.DryRun {
		return time.Time{}, nil
	}
	return ReadExpiry(d.Paths.LetsEncryptCert(r.CertName))
}

// lastLines is the last n non-empty lines of out.
func lastLines(out string, n int) string {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	return strings.Join(lines[max(len(lines)-n, 0):], "\n")
}

// ReadExpiry returns the expiry of the first certificate in a PEM file.
func ReadExpiry(certPath string) (time.Time, error) {
	b, err := os.ReadFile(certPath)
	if err != nil {
		return time.Time{}, err
	}
	block, _ := pem.Decode(b)
	if block == nil || block.Type != "CERTIFICATE" {
		return time.Time{}, fmt.Errorf("%s holds no PEM certificate", certPath)
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return time.Time{}, fmt.Errorf("%s: %w", certPath, err)
	}
	return cert.NotAfter, nil
}
