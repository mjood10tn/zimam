package rclone

import (
	"crypto/aes"
	"crypto/cipher"
	"encoding/base64"
	"errors"
)

// obscureKey is rclone's fixed key for "obscured" config values
// (fs/config/obscure). Obscuring hides a value from a glance; it is not
// encryption: anyone with rclone.conf can reveal it, which is why the file is
// root 0600 (R05-D2).
var obscureKey = []byte{
	0x9c, 0x93, 0x5b, 0x48, 0x73, 0x0a, 0x55, 0x4d, 0x6b, 0xfd, 0x7c, 0x63, 0xc8, 0x86, 0xa9, 0x2b,
	0xd3, 0x90, 0x19, 0x8e, 0xb8, 0x12, 0x8a, 0xfb, 0xf4, 0xde, 0x16, 0x2b, 0x8b, 0x95, 0xf6, 0x38,
}

// ivSize is AES's block size: rclone prefixes the IV to the ciphertext.
const ivSize = aes.BlockSize

func crypt(iv, in []byte) []byte {
	block, err := aes.NewCipher(obscureKey)
	if err != nil {
		panic(err) // the key is a constant of the right size
	}
	out := make([]byte, len(in))
	cipher.NewCTR(block, iv).XORKeyStream(out, in)
	return out
}

// Obscure gives plain in rclone's obscured form: AES-256-CTR under the
// fixed key with iv (16 random bytes), as base64url without padding of the
// IV followed by the ciphertext. Callers pass a fresh random IV each time.
func Obscure(plain string, iv []byte) string {
	if len(iv) != ivSize {
		panic("rclone: the IV must be 16 bytes")
	}
	return base64.RawURLEncoding.EncodeToString(append(append([]byte{}, iv...), crypt(iv, []byte(plain))...))
}

// Reveal inverts Obscure, as `rclone reveal` does.
func Reveal(obscured string) (string, error) {
	raw, err := base64.RawURLEncoding.DecodeString(obscured)
	if err != nil {
		return "", errors.New("rclone: not an obscured value")
	}
	if len(raw) <= ivSize {
		return "", errors.New("rclone: obscured value too short")
	}
	return string(crypt(raw[:ivSize], raw[ivSize:])), nil
}
