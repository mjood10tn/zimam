package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// signedDist builds a signed release directory whose manifest expires at
// expires, and returns it with the private and public key files.
func signedDist(t *testing.T, expires time.Time) (dir, private, public string) {
	t.Helper()
	private, public = keyPair(t)
	dir = t.TempDir()
	writeFile(t, filepath.Join(dir, "zimam-agent-linux-amd64"), "agent")
	writeFile(t, filepath.Join(dir, "install.sh"), "#!/bin/bash\n")
	mustRun(t, "manifest", "-version", "0.1.0", "-expires", expires.UTC().Format(time.RFC3339),
		"-composer", composerFiles(t, "8.3", "8.3"), "-out", filepath.Join(dir, "manifest.json"),
		filepath.Join(dir, "zimam-agent-linux-amd64"), filepath.Join(dir, "install.sh"))
	mustRun(t, "sign", "-key", private, filepath.Join(dir, "manifest.json"))
	return dir, private, public
}

func readBoth(t *testing.T, dir string) (manifest, sig string) {
	t.Helper()
	m, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	s, err := os.ReadFile(filepath.Join(dir, "manifest.json.sig"))
	if err != nil {
		t.Fatal(err)
	}
	return string(m), string(s)
}

var expiresField = regexp.MustCompile(`"expires": "[^"]*"`)

func TestResignOnlyChangesExpiry(t *testing.T) {
	dir, private, _ := signedDist(t, time.Now().Add(time.Hour))
	before, _ := readBoth(t, dir)
	start := time.Now().UTC().Truncate(time.Second)
	mustRun(t, "resign", "-key", private, "-days", "30", dir)
	after, _ := readBoth(t, dir)

	var m struct {
		Expires string `json:"expires"`
	}
	if err := json.Unmarshal([]byte(after), &m); err != nil {
		t.Fatal(err)
	}
	exp, err := time.Parse(time.RFC3339, m.Expires)
	if err != nil {
		t.Fatal(err)
	}
	// The same form build-release.sh writes: UTC, whole seconds.
	if want := exp.UTC().Format(time.RFC3339); m.Expires != want {
		t.Fatalf("expires is %q, want the form %q", m.Expires, want)
	}
	if lo, hi := start.Add(30*24*time.Hour), time.Now().Add(30*24*time.Hour); exp.Before(lo) || exp.After(hi) {
		t.Fatalf("expires %s, want 30 days from now (%s to %s)", exp, lo, hi)
	}
	if got, want := expiresField.ReplaceAllString(after, "EXPIRES"), expiresField.ReplaceAllString(before, "EXPIRES"); got != want {
		t.Fatalf("resign changed more than expires:\nbefore:\n%s\nafter:\n%s", before, after)
	}
	if strings.Count(before, `"expires"`) != 1 {
		t.Fatalf("the test manifest has %d expires fields", strings.Count(before, `"expires"`))
	}
}

func TestResignRefusesChangedArtifact(t *testing.T) {
	dir, private, _ := signedDist(t, time.Now().Add(time.Hour))
	writeFile(t, filepath.Join(dir, "install.sh"), "#!/bin/sh\n")
	beforeM, beforeS := readBoth(t, dir)
	_, stderr, code := runTool(t, "resign", "-key", private, "-days", "30", dir)
	if code == 0 || !strings.Contains(stderr, "install.sh") {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if afterM, afterS := readBoth(t, dir); afterM != beforeM || afterS != beforeS {
		t.Fatal("resign changed the manifest of a release it refused")
	}
}

func TestResignRefusesExtraFile(t *testing.T) {
	dir, private, _ := signedDist(t, time.Now().Add(time.Hour))
	writeFile(t, filepath.Join(dir, "install-extra.sh"), "#!/bin/sh\n")
	beforeM, beforeS := readBoth(t, dir)
	_, stderr, code := runTool(t, "resign", "-key", private, "-days", "30", dir)
	if code == 0 || !strings.Contains(stderr, "install-extra.sh") {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if afterM, afterS := readBoth(t, dir); afterM != beforeM || afterS != beforeS {
		t.Fatal("resign changed the manifest of a release it refused")
	}
}

func TestResignedManifestVerifies(t *testing.T) {
	for name, expires := range map[string]time.Time{
		"live":    time.Now().Add(time.Hour),
		"expired": time.Now().Add(-48 * time.Hour),
	} {
		t.Run(name, func(t *testing.T) {
			dir, private, public := signedDist(t, expires)
			mustRun(t, "resign", "-key", private, "-days", "30", dir)
			if out := mustRun(t, "verify", "-key", public, "-agent-version", "0.1.0", dir); !strings.Contains(out, "0.1.0") {
				t.Fatalf("verify printed %q", out)
			}
		})
	}
}

// resign signs only what the key it holds already signed: a manifest under
// another key, or one changed after signing, is refused untouched.
func TestResignRefusesBadSignature(t *testing.T) {
	for name, tamper := range map[string]func(dir string){
		"changed manifest": func(dir string) {
			name := filepath.Join(dir, "manifest.json")
			data, _ := os.ReadFile(name)
			writeFile(t, name, strings.Replace(string(data), "0.1.0", "0.1.1", 1))
		},
		"missing signature": func(dir string) { os.Remove(filepath.Join(dir, "manifest.json.sig")) },
	} {
		dir, private, _ := signedDist(t, time.Now().Add(time.Hour))
		tamper(dir)
		if _, _, code := runTool(t, "resign", "-key", private, "-days", "30", dir); code == 0 {
			t.Errorf("%s: resign exited 0", name)
		}
	}
	dir, _, _ := signedDist(t, time.Now().Add(time.Hour))
	other, _ := keyPair(t)
	beforeM, beforeS := readBoth(t, dir)
	if _, _, code := runTool(t, "resign", "-key", other, "-days", "30", dir); code == 0 {
		t.Fatal("resign accepted a manifest signed by another key")
	}
	if afterM, afterS := readBoth(t, dir); afterM != beforeM || afterS != beforeS {
		t.Fatal("resign changed a manifest signed by another key")
	}
}

// A manifest in any other form than the tool writes can't be re-signed with
// only its expiry changed.
func TestResignRefusesOtherFormatting(t *testing.T) {
	dir, private, _ := signedDist(t, time.Now().Add(time.Hour))
	name := filepath.Join(dir, "manifest.json")
	data, _ := os.ReadFile(name)
	writeFile(t, name, strings.TrimSuffix(string(data), "\n"))
	mustRun(t, "sign", "-key", private, name)
	if _, stderr, code := runTool(t, "resign", "-key", private, "-days", "30", dir); code == 0 || !strings.Contains(stderr, "form") {
		t.Fatalf("exit %d: %s", code, stderr)
	}
}

func TestResignUsage(t *testing.T) {
	dir, private, _ := signedDist(t, time.Now().Add(time.Hour))
	for _, args := range [][]string{
		{"resign", dir},
		{"resign", "-key", private, dir},
		{"resign", "-key", private, "-days", "0", dir},
		{"resign", "-key", private, "-days", "31", dir},
		{"resign", "-key", private, "-days", "30"},
	} {
		if _, stderr, code := runTool(t, args...); code != 2 || !strings.Contains(stderr, "usage") {
			t.Errorf("release %s: exit %d: %s", strings.Join(args, " "), code, stderr)
		}
	}
}
