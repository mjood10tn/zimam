package backup

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"zimam/agent/contract"
	"zimam/agent/internal/sysexec"
)

const sshKeyscan = "/usr/bin/ssh-keyscan"

// keyAlgorithms maps a host key type to the host_key_algorithms rclone gets
// for it (R05-D2), in order of preference when the owner named no key.
var keyAlgorithms = []struct{ typ, algorithms string }{
	{"ssh-ed25519", "ssh-ed25519"},
	{"ecdsa-sha2-nistp256", "ecdsa-sha2-nistp256"},
	{"ecdsa-sha2-nistp384", "ecdsa-sha2-nistp384"},
	{"ecdsa-sha2-nistp521", "ecdsa-sha2-nistp521"},
	{"ssh-rsa", "rsa-sha2-512 rsa-sha2-256"},
}

// hostKeyLine is one host key from ssh-keyscan.
type hostKeyLine struct {
	typ         string
	blob        string // base64, as known_hosts holds it
	fingerprint contract.HostKeyFingerprint
	algorithms  string
	rank        int
}

// knownHosts is the key's known_hosts line; a port other than 22 is named
// as OpenSSH does it.
func (k hostKeyLine) knownHosts(host contract.SFTPHost, port contract.TCPPort) string {
	name := string(host)
	if port != 22 {
		name = "[" + name + "]:" + strconv.Itoa(int(port))
	}
	return name + " " + k.typ + " " + k.blob + "\n"
}

// parseKeyscan reads ssh-keyscan's "host type base64" lines, keeping the
// keys of a known type whose blob is well formed and names that type.
func parseKeyscan(out []byte) ([]hostKeyLine, error) {
	var keys []hostKeyLine
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) != 3 || strings.HasPrefix(f[0], "#") {
			continue
		}
		rank := slices.IndexFunc(keyAlgorithms, func(a struct{ typ, algorithms string }) bool { return a.typ == f[1] })
		blob, err := base64.StdEncoding.DecodeString(f[2])
		if rank < 0 || err != nil || len(blob) < 4 {
			continue
		}
		// The blob starts with its own type as an SSH string.
		n := binary.BigEndian.Uint32(blob)
		if uint64(n) > uint64(len(blob)-4) || string(blob[4:4+n]) != f[1] {
			continue
		}
		sum := sha256.Sum256(blob)
		keys = append(keys, hostKeyLine{typ: f[1], blob: f[2], rank: rank, algorithms: keyAlgorithms[rank].algorithms,
			fingerprint: contract.HostKeyFingerprint("SHA256:" + base64.RawStdEncoding.EncodeToString(sum[:]))})
	}
	return keys, sc.Err()
}

// pickHostKey chooses the key to pin: the owner's, when a fingerprint is
// given, else the preferred type (OQ3).
func pickHostKey(keys []hostKeyLine, want contract.HostKeyFingerprint) (hostKeyLine, error) {
	if want != "" {
		for _, k := range keys {
			if k.fingerprint == want {
				return k, nil
			}
		}
		var got []string
		for _, k := range keys {
			got = append(got, string(k.fingerprint))
		}
		return hostKeyLine{}, fmt.Errorf("the server's host keys (%s) don't include the expected %s", strings.Join(got, ", "), want)
	}
	if len(keys) == 0 {
		return hostKeyLine{}, fmt.Errorf("no host key of a known type")
	}
	return slices.MinFunc(keys, func(a, b hostKeyLine) int { return a.rank - b.rank }), nil
}

// scanHostKeys asks the server for its host keys once. An empty answer is
// an error: OpenSSH's PerSourcePenalties can refuse repeated scans for a
// while, and that must never read as a server without keys (Task 1).
func scanHostKeys(ctx context.Context, d sysexec.Deps, host contract.SFTPHost, port contract.TCPPort) ([]hostKeyLine, error) {
	res, err := d.Run.Run(ctx, sysexec.Cmd{Path: sshKeyscan, Args: []string{"-T", "10", "-p", strconv.Itoa(int(port)), "-t", "ed25519,ecdsa,rsa", string(host)}})
	if err != nil {
		return nil, fmt.Errorf("scanning %s's host keys: %w", host, err)
	}
	keys, err := parseKeyscan(res.Stdout)
	if err != nil {
		return nil, err
	}
	if len(keys) == 0 {
		return nil, fmt.Errorf("no host key received from %s port %d; check the host and port, and try again in a minute", host, port)
	}
	return keys, nil
}
