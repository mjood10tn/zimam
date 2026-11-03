package sites

import (
	"context"
	"errors"
	"fmt"
	"io/fs"

	"zimam/agent/contract"
	"zimam/agent/internal/acme"
	"zimam/agent/internal/sysexec"
)

// CheckCertificate is cert.issue's precheck: not_found for a site that
// doesn't exist. A site whose provisioning never finished has no state
// file, and nothing to serve over HTTPS yet.
func CheckCertificate(d sysexec.Deps, name contract.SiteName) error {
	_, err := Load(d.Paths, name)
	if errors.Is(err, fs.ErrNotExist) {
		return &contract.AgentError{Code: contract.ErrNotFound, Message: fmt.Sprintf("Site %s doesn't exist.", name)}
	}
	return err
}

// IssueCertificate gets the site a certificate over HTTP-01 (§10), then
// turns on force_https and serves the site over HTTPS, with the HTTP server
// redirecting. If certbot fails, the site keeps its HTTP vhost and the
// error carries certbot's last lines. If Nginx refuses the HTTPS vhost, the
// HTTP one is restored and force_https stays off; a retry finds the
// certificate already there, which certbot keeps without a new order.
//
// acme.Issue runs certbot, whose deploy hook takes the reload lock, so the
// reload lock is never held here while it runs.
func IssueCertificate(ctx context.Context, d sysexec.Deps, name contract.SiteName) (contract.CertIssueResult, error) {
	st, err := Load(d.Paths, name)
	if err != nil {
		return contract.CertIssueResult{}, err
	}
	certName := "zimam-" + string(name)
	expiry, err := acme.Issue(ctx, d, acme.Request{CertName: certName, Domain: st.Domain})
	if err != nil {
		return contract.CertIssueResult{}, err
	}
	next := st
	next.Security.ForceHTTPS = true
	d.Logf("Switching %s to HTTPS", name)
	if err := ensureVhost(ctx, d, next); err != nil {
		return contract.CertIssueResult{}, err
	}
	if err := Save(d.Paths, next); err != nil {
		return contract.CertIssueResult{}, err
	}
	if err := acme.WriteRecord(d, string(name), expiry); err != nil {
		return contract.CertIssueResult{}, err
	}
	d.Logf("%s's certificate expires on %s", name, expiry.UTC().Format("2006-01-02"))
	return contract.CertIssueResult{ExpiresAt: expiry.UTC()}, nil
}
