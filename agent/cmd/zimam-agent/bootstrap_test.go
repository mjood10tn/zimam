package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"strings"
	"testing"
)

func TestBootstrapNeedsPanelDomain(t *testing.T) {
	for _, args := range [][]string{
		nil,
		{"--panel-domain", "not a domain"},
		{"--panel-domain", "panel.example.com", "--php", "7.4"},
		{"--panel-domain", "panel.example.com", "--version", "v1"},
		{"--panel-domain", "panel.example.com", "--other"},
		{"--panel-domain", "panel.example.com", "extra"},
	} {
		code, stdout, stderr := runCapture(append([]string{"bootstrap"}, args...))
		if code != exitUsage || stdout != "" || !strings.Contains(stderr, "usage: zimam-agent bootstrap") {
			t.Errorf("bootstrap %q: exit %d, stdout %q, stderr %q; want usage", args, code, stdout, stderr)
		}
	}
}

func TestBootstrapParsesEveryFlag(t *testing.T) {
	var stderr bytes.Buffer
	o, ok := bootstrapOptions([]string{
		"--panel-domain", "panel.example.com", "--email", "admin@example.com", "--php", "8.4", "--version", "1.2.3",
		"--acme-server", "https://pebble:14000/dir", "--acme-ca-bundle", "/root/pebble-ca.pem", "--yes",
	}, &stderr)
	if !ok {
		t.Fatalf("refused: %s", stderr.String())
	}
	if o.PanelDomain != "panel.example.com" || o.Email != "admin@example.com" || o.PHP != "8.4" || o.Version != "1.2.3" ||
		o.ACMEServer != "https://pebble:14000/dir" || o.ACMECABundle != "/root/pebble-ca.pem" {
		t.Fatalf("options %+v", o)
	}
}

func TestReleaseKey(t *testing.T) {
	if _, err := releaseKey(""); err == nil {
		t.Error("a development build's empty key was accepted")
	}
	if _, err := releaseKey(base64.StdEncoding.EncodeToString([]byte("short"))); err == nil {
		t.Error("a 5-byte key was accepted")
	}
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	got, err := releaseKey(base64.StdEncoding.EncodeToString(pub))
	if err != nil || !got.Equal(pub) {
		t.Errorf("releaseKey = %x, %v; want %x", got, err, pub)
	}
}
