package main

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"os/user"
	"strconv"
	"syscall"
	"time"

	"zimam/agent/contract"
	"zimam/agent/internal/audit"
	"zimam/agent/internal/bootstrap"
	"zimam/agent/internal/helper"
	"zimam/agent/internal/release"
	"zimam/agent/internal/sysexec"
)

const bootstrapUsage = "usage: zimam-agent bootstrap --panel-domain <fqdn> [--email <address>] [--php <x.y>] [--version <x.y.z>] [--acme-server <url>] [--acme-ca-bundle <path>] [--yes]"

// runBootstrap is `zimam-agent bootstrap` (design §4), which install.sh
// runs last. Progress goes to stderr; stdout gets only the setup link, so
// it is the installer's last line.
func runBootstrap(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	o, ok := bootstrapOptions(args, stderr)
	if !ok {
		fmt.Fprintln(stderr, bootstrapUsage)
		return exitUsage
	}
	fail := func(err error) int {
		fmt.Fprintf(stderr, "zimam-agent bootstrap: %v\n", err)
		return 1
	}
	if os.Geteuid() != 0 {
		return fail(errors.New("must run as root"))
	}
	pub, err := releaseKey(release.PublicKey)
	if err != nil {
		return fail(err)
	}
	zimam, err := helper.LookupAccount("zimam")
	if err != nil {
		return fail(fmt.Errorf("%w (install.sh creates the zimam user)", err))
	}
	agentGID, err := groupID("zimam-agent")
	if err != nil {
		return fail(err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()
	p := systemPaths()
	b := &bootstrap.Bootstrap{
		Deps: sysexec.Deps{
			Paths: p,
			Run:   sysexec.ExecRunner{},
			Logf:  func(format string, args ...any) { fmt.Fprintf(stderr, format+"\n", args...) },
			Now:   time.Now,
		},
		Helper:       &helper.Client{AgentBinary: p.AgentBinary()},
		Audit:        audit.Writer{Paths: p},
		Zimam:        zimam,
		IDs:          bootstrap.IDs{RootUID: 0, RootGID: 0, AgentGID: agentGID},
		PublicKey:    pub,
		BaseURL:      release.BaseURL,
		AgentVersion: release.Version,
	}
	link, err := b.Run(ctx, o)
	if err != nil {
		fmt.Fprintf(stderr, "zimam-agent bootstrap: %v\n", err)
		if se := (*bootstrap.StepError)(nil); errors.As(err, &se) && se.Hint != "" {
			fmt.Fprintf(stderr, "Hint: %s\n", se.Hint)
		}
		return 1
	}
	fmt.Fprintln(stdout, link)
	return 0
}

// bootstrapOptions parses and checks the flags. --version defaults to this
// agent's own release, so the agent and the panel come from one release.
func bootstrapOptions(args []string, stderr io.Writer) (bootstrap.Options, bool) {
	fs := flag.NewFlagSet("bootstrap", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	domain := fs.String("panel-domain", "", "")
	email := fs.String("email", "", "")
	php := fs.String("php", "", "")
	version := fs.String("version", release.Version, "")
	acmeServer := fs.String("acme-server", "", "")
	caBundle := fs.String("acme-ca-bundle", "", "")
	fs.Bool("yes", false, "") // bootstrap never prompts
	if err := fs.Parse(args); err != nil {
		fmt.Fprintf(stderr, "zimam-agent bootstrap: %v\n", err)
		return bootstrap.Options{}, false
	}
	o := bootstrap.Options{
		PanelDomain:  contract.Domain(*domain),
		Email:        *email,
		PHP:          contract.PHPVersion(*php),
		Version:      *version,
		ACMEServer:   *acmeServer,
		ACMECABundle: *caBundle,
	}
	var problems []string
	if fs.NArg() != 0 {
		problems = append(problems, fmt.Sprintf("unexpected argument %q", fs.Arg(0)))
	}
	if err := o.PanelDomain.Validate(); err != nil {
		problems = append(problems, fmt.Sprintf("--panel-domain %q %v", *domain, err))
	}
	if *php != "" {
		if err := o.PHP.Validate(); err != nil {
			problems = append(problems, fmt.Sprintf("--php %q %v", *php, err))
		}
	}
	if !release.ValidVersion(o.Version) {
		problems = append(problems, fmt.Sprintf("--version %q isn't a SemVer version", o.Version))
	}
	for _, p := range problems {
		fmt.Fprintf(stderr, "zimam-agent bootstrap: %s\n", p)
	}
	return o, len(problems) == 0
}

// releaseKey decodes the release key the build stamped into the agent. A
// development build has none.
func releaseKey(stamped string) (ed25519.PublicKey, error) {
	raw, err := base64.StdEncoding.DecodeString(stamped)
	if err != nil || len(raw) != ed25519.PublicKeySize {
		return nil, errors.New("this agent carries no release key (a development build), so it can't verify a release")
	}
	return ed25519.PublicKey(raw), nil
}

func groupID(name string) (int, error) {
	g, err := user.LookupGroup(name)
	if err != nil {
		return 0, fmt.Errorf("%w (install.sh creates the %s group)", err, name)
	}
	return strconv.Atoi(g.Gid)
}
