package php

import (
	"bytes"
	"crypto/sha1"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

// SuryFingerprint is the primary key fingerprint of packages.sury.org's
// signing key (§8.2). sury publishes none, so it was checked on 2026-10-27
// against apt.gpg, sury's debsuryorg-archive-keyring package and
// keyserver.ubuntu.com, which all agree.
const SuryFingerprint = "15058500A0235D97F5D10063B188E2B695BD4743"

const (
	tagPublicKey = 6
	armorBegin   = "-----BEGIN PGP PUBLIC KEY BLOCK-----"
	armorEnd     = "-----END PGP PUBLIC KEY BLOCK-----"
)

var errTruncated = errors.New("the keyring is truncated")

// PrimaryFingerprint returns the fingerprint of the one primary key in an
// OpenPGP keyring, binary or ASCII-armored, as 40 upper-case hex digits.
func PrimaryFingerprint(keyring []byte) (string, error) {
	_, fpr, err := readKeyring(keyring)
	return fpr, err
}

// readKeyring returns the keyring in binary form, and its primary key's
// fingerprint. The keyring must hold exactly one primary key, first: APT
// trusts every key in a Signed-By keyring, so a second one would be trusted
// on the strength of the first one's fingerprint. Only v4 keys are read, the
// version sury's key has (RFC 4880 §12.2).
func readKeyring(keyring []byte) ([]byte, string, error) {
	data := keyring
	if bytes.HasPrefix(bytes.TrimSpace(keyring), []byte(armorBegin)) {
		var err error
		if data, err = dearmor(keyring); err != nil {
			return nil, "", err
		}
	}
	var primary []byte
	primaries := 0
	err := packets(data, func(tag byte, body []byte) error {
		if primaries == 0 && tag != tagPublicKey {
			return fmt.Errorf("the keyring starts with packet type %d, not a public key", tag)
		}
		if tag == tagPublicKey {
			primaries++
			primary = body
		}
		return nil
	})
	switch {
	case err != nil:
		return nil, "", err
	case primaries > 1:
		return nil, "", fmt.Errorf("the keyring holds %d primary keys; only one is allowed", primaries)
	case len(primary) == 0:
		return nil, "", errors.New("the keyring's public key packet is empty")
	case primary[0] != 4:
		return nil, "", fmt.Errorf("the keyring's key is version %d; only version 4 is supported", primary[0])
	case len(primary) > 0xffff:
		return nil, "", errors.New("the keyring's public key packet is too long for a v4 fingerprint")
	}
	h := sha1.New()
	h.Write([]byte{0x99, byte(len(primary) >> 8), byte(len(primary))})
	h.Write(primary)
	return data, strings.ToUpper(hex.EncodeToString(h.Sum(nil))), nil
}

// packets calls fn with each packet's tag and body, in old or new format
// (RFC 4880 §4.2).
func packets(data []byte, fn func(tag byte, body []byte) error) error {
	if len(data) == 0 {
		return errors.New("the keyring is empty")
	}
	for len(data) > 0 {
		b := data[0]
		if b&0x80 == 0 {
			return errors.New("the keyring isn't OpenPGP data")
		}
		var tag byte
		var hdr, n int
		if b&0x40 != 0 {
			tag = b & 0x3f
			if len(data) < 2 {
				return errTruncated
			}
			switch l := int(data[1]); {
			case l < 192:
				hdr, n = 2, l
			case l < 224:
				if len(data) < 3 {
					return errTruncated
				}
				hdr, n = 3, (l-192)<<8+int(data[2])+192
			case l == 255:
				if len(data) < 6 {
					return errTruncated
				}
				hdr, n = 6, int(binary.BigEndian.Uint32(data[2:6]))
			default:
				return errors.New("the keyring uses partial body lengths, which key packets never do")
			}
		} else {
			tag = (b >> 2) & 0x0f
			switch b & 3 {
			case 0:
				if len(data) < 2 {
					return errTruncated
				}
				hdr, n = 2, int(data[1])
			case 1:
				if len(data) < 3 {
					return errTruncated
				}
				hdr, n = 3, int(binary.BigEndian.Uint16(data[1:3]))
			case 2:
				if len(data) < 5 {
					return errTruncated
				}
				hdr, n = 5, int(binary.BigEndian.Uint32(data[1:5]))
			default:
				return errors.New("the keyring has a packet of indeterminate length")
			}
		}
		if n > len(data)-hdr {
			return errTruncated
		}
		if err := fn(tag, data[hdr:hdr+n]); err != nil {
			return err
		}
		data = data[hdr+n:]
	}
	return nil
}

// dearmor decodes an ASCII-armored public key block (RFC 4880 §6.2). It
// skips the checksum line: RFC 9580 tells readers not to rely on it, and the
// fingerprint check that follows covers the content.
func dearmor(armored []byte) ([]byte, error) {
	// Every line is trimmed, which also drops a CRLF line's "\r".
	lines := strings.Split(string(armored), "\n")
	i := 0
	for i < len(lines) && strings.TrimSpace(lines[i]) != armorBegin {
		i++
	}
	// Armor headers ("Key: value") come before the blank line.
	for i++; i < len(lines) && strings.Contains(lines[i], ": "); i++ {
	}
	var body strings.Builder
	for ; i < len(lines); i++ {
		switch l := strings.TrimSpace(lines[i]); {
		case l == armorEnd:
			data, err := base64.StdEncoding.DecodeString(body.String())
			if err != nil {
				return nil, fmt.Errorf("the armored keyring isn't valid base64: %v", err)
			}
			return data, nil
		case !strings.HasPrefix(l, "="):
			body.WriteString(l)
		}
	}
	return nil, errors.New("the armored keyring has no END line")
}
