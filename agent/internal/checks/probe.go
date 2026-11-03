// Package checks runs security.check's site and server checks (design
// §9.7). It only reads, except for the dotfile canary, which the helper
// writes and removes inside the site's home.
package checks

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"time"

	"zimam/agent/internal/sysexec"
)

// Probe makes the site checks' requests to this server: to 127.0.0.1, over
// HTTPS with the site's domain as SNI once a certificate exists, otherwise
// over HTTP with the site's Host header. The TLS chain isn't verified:
// certificate_valid reads the certificate file itself. Redirects are never
// followed. The addresses are fields so tests can point them elsewhere.
type Probe struct {
	HTTP, HTTPS string // host:port
}

// DefaultProbe is the server's own ports 80 and 443.
var DefaultProbe = Probe{HTTP: "127.0.0.1:80", HTTPS: "127.0.0.1:443"}

const probeTimeout = 10 * time.Second

// Get requests path from domain and answers the status and Location.
func (p Probe) Get(ctx context.Context, https bool, domain, path string) (int, string, error) {
	addr, scheme := p.HTTP, "http"
	if https {
		addr, scheme = p.HTTPS, "https"
	}
	dialer := &net.Dialer{Timeout: probeTimeout}
	client := &http.Client{
		Timeout: probeTimeout,
		Transport: &http.Transport{
			Proxy:             nil,
			DisableKeepAlives: true,
			DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
				return dialer.DialContext(ctx, network, addr)
			},
			TLSClientConfig: &tls.Config{ServerName: domain, InsecureSkipVerify: true}, //nolint:gosec // the chain is checked from the file, not here
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, scheme+"://"+domain+path, nil)
	if err != nil {
		return 0, "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, "", err
	}
	resp.Body.Close()
	return resp.StatusCode, resp.Header.Get("Location"), nil
}

func sysexecCmd(path string, args ...string) sysexec.Cmd { return sysexec.Cmd{Path: path, Args: args} }
