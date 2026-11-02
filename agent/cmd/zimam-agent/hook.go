package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"zimam/agent/contract"
	"zimam/agent/internal/acme"
	"zimam/agent/internal/audit"
	"zimam/agent/internal/nginx"
	"zimam/agent/internal/paths"
	"zimam/agent/internal/sysexec"
)

// lineagePrefix is where certbot keeps Zimam's certificates: zimam-panel
// and zimam-<site>.
const lineagePrefix = "/etc/letsencrypt/live/zimam-"

// hookCertDeployed is `zimam-agent hook cert-deployed`, certbot's deploy
// hook for Zimam's certificates (design §10). certbot names the renewed
// certificate in RENEWED_LINEAGE.
func hookCertDeployed(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) != 0 {
		fmt.Fprintln(stderr, "usage: zimam-agent hook cert-deployed (run by certbot, with RENEWED_LINEAGE set)")
		return exitUsage
	}
	p := systemPaths()
	d := sysexec.Deps{
		Paths: p,
		Run:   sysexec.ExecRunner{},
		Logf:  func(format string, args ...any) { fmt.Fprintf(stderr, format+"\n", args...) },
		Now:   time.Now,
	}
	return certDeployed(context.Background(), d, os.Getenv, audit.Writer{Paths: p}, stderr)
}

// certDeployed tests and reloads Nginx so it serves the renewed
// certificate, writes the new expiry record, and audits the run as cli:root.
// It refuses any lineage but the panel's or a site's that has a state file.
func certDeployed(ctx context.Context, d sysexec.Deps, getenv func(string) string, w audit.Writer, stderr io.Writer) int {
	start := d.Now()
	lineage := getenv("RENEWED_LINEAGE")
	finish := func(outcome string) error {
		return w.Write(audit.Entry{
			TS:      start,
			Actor:   "cli:root",
			Command: "hook cert-deployed",
			Params: audit.Redact(struct {
				Lineage string `json:"lineage"`
			}{lineage}),
			Outcome:    outcome,
			DurationMS: d.Now().Sub(start).Milliseconds(),
		})
	}
	fail := func(code contract.ErrorCode, err error) int {
		fmt.Fprintf(stderr, "zimam-agent hook cert-deployed: %v\n", err)
		if aerr := finish("error:" + string(code)); aerr != nil {
			fmt.Fprintf(stderr, "zimam-agent hook cert-deployed: %v\n", aerr)
		}
		return 1
	}
	// The reload and audit locks live in /run/zimam/locks. /run is emptied
	// at boot, and certbot's timer may run this before the agent has made it.
	if err := os.MkdirAll(d.Paths.LocksDir(), 0o700); err != nil {
		return fail(contract.ErrInternal, err)
	}
	name, err := lineageName(d.Paths, lineage)
	if err != nil {
		return fail(contract.ErrValidationFailed, err)
	}
	// With no changes, ApplySite tests Nginx and reloads it under one hold
	// of the reload lock.
	if err := nginx.ApplySite(ctx, d, nil, nginx.Link{}); err != nil {
		return fail(contract.ErrInternal, err)
	}
	expiry, err := acme.ReadExpiry(d.Paths.LetsEncryptCert("zimam-" + name))
	if err != nil {
		return fail(contract.ErrInternal, err)
	}
	if err := acme.WriteRecord(d, name, expiry); err != nil {
		return fail(contract.ErrInternal, err)
	}
	d.Logf("zimam-%s renewed; it expires on %s", name, expiry.UTC().Format(time.DateOnly))
	if err := finish("ok"); err != nil {
		fmt.Fprintf(stderr, "zimam-agent hook cert-deployed: %v\n", err)
		return 1
	}
	return 0
}

// lineageName reads the certificate name out of RENEWED_LINEAGE: "panel",
// or the name of a site that has a state file. The value is only data; it
// is never used as a path.
func lineageName(p paths.Paths, lineage string) (string, error) {
	name, ok := strings.CutPrefix(lineage, lineagePrefix)
	if ok && name == "panel" {
		return name, nil
	}
	if ok && contract.SiteName(name).Validate() == nil {
		if fi, err := os.Lstat(p.SiteState(contract.SiteName(name))); err == nil && fi.Mode().IsRegular() {
			return name, nil
		}
	}
	return "", fmt.Errorf("RENEWED_LINEAGE %q is neither the panel's certificate nor a site's", lineage)
}
