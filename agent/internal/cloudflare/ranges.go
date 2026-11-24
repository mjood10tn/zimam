// Package cloudflare is Cloudflare mode (R06-D7, D9): Nginx takes the
// visitor's address from CF-Connecting-IP on connections from Cloudflare's
// ranges, and 80 and 443 can be opened to those ranges only. The ranges are
// compiled in and refreshed weekly by maintenance.
package cloudflare

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/netip"
	"os"
	"slices"
	"strings"
	"time"

	"zimam/agent/contract"
	"zimam/agent/internal/configtx"
	"zimam/agent/internal/fetch"
	"zimam/agent/internal/firewall"
	"zimam/agent/internal/paths"
	"zimam/agent/internal/sysexec"
)

// Cloudflare's published lists: text/plain, one CIDR per line (spike).
const (
	URLv4 = "https://www.cloudflare.com/ips-v4"
	URLv6 = "https://www.cloudflare.com/ips-v6"
)

// compiledAt is when compiledV4 and compiledV6 were copied from those lists.
var compiledAt = time.Date(2026, 11, 24, 0, 0, 0, 0, time.UTC)

var (
	compiledV4 = []string{
		"173.245.48.0/20", "103.21.244.0/22", "103.22.200.0/22", "103.31.4.0/22", "141.101.64.0/18",
		"108.162.192.0/18", "190.93.240.0/20", "188.114.96.0/20", "197.234.240.0/22", "198.41.128.0/17",
		"162.158.0.0/15", "104.16.0.0/13", "104.24.0.0/14", "172.64.0.0/13", "131.0.72.0/22",
	}
	compiledV6 = []string{
		"2400:cb00::/32", "2606:4700::/32", "2803:f800::/32", "2405:b500::/32", "2405:8100::/32",
		"2a06:98c0::/29", "2c0f:f248::/32",
	}
)

const (
	refreshEvery = 7 * 24 * time.Hour
	// R06-D7's bounds on a fetched list.
	minRanges, maxRanges = 5, 100
	maxBody              = 64 << 10
)

// Ranges are Cloudflare's addresses, each family apart.
type Ranges struct {
	V4 []netip.Prefix `json:"v4"`
	V6 []netip.Prefix `json:"v6"`
}

// All lists both families, as firewall sources.
func (r Ranges) All() contract.IPList {
	var out contract.IPList
	for _, p := range append(slices.Clone(r.V4), r.V6...) {
		out = append(out, p.String())
	}
	return out
}

func (r Ranges) prefixes() []netip.Prefix { return append(slices.Clone(r.V4), r.V6...) }

func compiled() Ranges {
	parse := func(list []string) []netip.Prefix {
		out := make([]netip.Prefix, len(list))
		for i, s := range list {
			out[i] = netip.MustParsePrefix(s)
		}
		return out
	}
	return Ranges{V4: parse(compiledV4), V6: parse(compiledV6)}
}

// state is cloudflare-ranges.json: the ranges last fetched, and maintenance's
// last try. Without a fetched list, the compiled one is in use.
type state struct {
	Ranges
	RefreshedAt time.Time `json:"refreshed_at,omitzero"`
	AttemptedAt time.Time `json:"attempted_at,omitzero"`
	Error       string    `json:"error,omitempty"`
}

func read(p paths.Paths) (state, error) {
	var s state
	b, err := os.ReadFile(p.CloudflareRanges())
	if errors.Is(err, fs.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	if err := json.Unmarshal(b, &s); err != nil {
		return s, fmt.Errorf("%s: %w", p.CloudflareRanges(), err)
	}
	return s, nil
}

func write(p paths.Paths, s state) error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return configtx.Apply(context.Background(), []configtx.Change{configtx.RootFile(p.CloudflareRanges(), append(b, '\n'), 0o600)},
		func(context.Context) error { return nil })
}

// Current returns the ranges in use and when they were fetched: the compiled
// list and its date until maintenance fetched one.
func Current(p paths.Paths) (Ranges, time.Time, error) {
	s, err := read(p)
	if err != nil {
		return Ranges{}, time.Time{}, err
	}
	if s.RefreshedAt.IsZero() {
		return compiled(), compiledAt, nil
	}
	return s.Ranges, s.RefreshedAt, nil
}

// Enabled reports whether Cloudflare mode is on: its realip file exists.
func Enabled(p paths.Paths) (bool, error) {
	_, err := os.Stat(p.NginxRealIPConf())
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}

// Refresh fetches Cloudflare's lists once a week while the mode is on, and
// applies them when they changed: the realip file, and the origin
// restriction if it is on (R06-D9). A list that fails a check, or a failed
// download, keeps the ranges in use; the failure is logged and recorded, and
// next week tries again. It reports whether the ranges changed.
func Refresh(ctx context.Context, d sysexec.Deps) (bool, error) {
	if on, err := Enabled(d.Paths); err != nil || !on {
		return false, err
	}
	s, err := read(d.Paths)
	if err != nil {
		return false, err
	}
	now := d.Now().UTC()
	if !s.AttemptedAt.After(now) && now.Sub(s.AttemptedAt) < refreshEvery {
		return false, nil
	}
	cur, _, err := Current(d.Paths)
	if err != nil {
		return false, err
	}
	s.AttemptedAt = now
	next, err := fetchRanges(ctx, d, cur)
	if err == nil && !slices.Equal(next.prefixes(), cur.prefixes()) {
		err = apply(ctx, d, next)
	}
	if err != nil {
		d.Logf("Refreshing Cloudflare's ranges failed; the ranges in use stay: %v", err)
		s.Error = err.Error()
		return false, write(d.Paths, s)
	}
	changed := !slices.Equal(next.prefixes(), cur.prefixes())
	s.Ranges, s.RefreshedAt, s.Error = next, now, ""
	return changed, write(d.Paths, s)
}

// apply puts new ranges in place: the realip file, then the web rules when
// the origin restriction is on. A refresh can't wait for the owner's
// confirmation, and it only swaps one set of Cloudflare's ranges for
// another, so it isn't guarded.
func apply(ctx context.Context, d sysexec.Deps, r Ranges) error {
	if err := writeRealIP(ctx, d, true, r); err != nil {
		return err
	}
	restricted, err := firewall.WebRestricted(d)
	if err != nil || !restricted {
		return err
	}
	return firewall.WebSources(ctx, d, r.All())
}

func fetchRanges(ctx context.Context, d sysexec.Deps, cur Ranges) (Ranges, error) {
	var r Ranges
	var err error
	if r.V4, err = fetchList(ctx, d, URLv4, false, cur.V4); err != nil {
		return r, err
	}
	r.V6, err = fetchList(ctx, d, URLv6, true, cur.V6)
	return r, err
}

func fetchList(ctx context.Context, d sysexec.Deps, url string, v6 bool, cur []netip.Prefix) ([]netip.Prefix, error) {
	var body strings.Builder
	if _, err := fetch.Download(ctx, fetch.HTTPSOnly(d.HTTPClient()), url, &body, maxBody); err != nil {
		return nil, err
	}
	out, err := parseList(body.String(), v6)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", url, err)
	}
	if 2*len(out) < len(cur) {
		return nil, fmt.Errorf("%s shrank from %d to %d ranges, more than half; refused", url, len(cur), len(out))
	}
	return out, nil
}

// parseList reads one list: every line a canonical CIDR of its family
// (§5.4), between minRanges and maxRanges of them.
func parseList(body string, v6 bool) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for line := range strings.SplitSeq(strings.TrimSuffix(body, "\n"), "\n") {
		p, err := contract.ParseIPEntry(line)
		if err != nil || !strings.Contains(line, "/") || p.String() != line || p.Addr().Is6() != v6 {
			return nil, fmt.Errorf("line %q isn't a canonical CIDR of its family", line)
		}
		out = append(out, p)
	}
	if len(out) < minRanges || len(out) > maxRanges {
		return nil, fmt.Errorf("has %d ranges, want %d to %d", len(out), minRanges, maxRanges)
	}
	return out, nil
}
