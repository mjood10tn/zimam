package rclone

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"zimam/agent/contract"
	"zimam/agent/internal/paths"
	"zimam/agent/internal/sysexec"
)

func sum(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// zipOf is a release zip holding binary at member, with a README beside it.
func zipOf(t *testing.T, member string, binary []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for name, body := range map[string][]byte{member: binary, "rclone-v9.9.9-linux-amd64/README.txt": []byte("readme")} {
		f, err := w.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		f.Write(body)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func testDeps(t *testing.T, srv *httptest.Server) sysexec.Deps {
	t.Helper()
	p := paths.Paths{Root: t.TempDir()}
	if err := os.MkdirAll(p.LocksDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	d := sysexec.Deps{Paths: p, Logf: t.Logf, Now: time.Now}
	if srv != nil {
		d.HTTP = srv.Client()
	}
	return d
}

const testMember = "rclone-v9.9.9-linux-amd64/rclone"

// serveRelease serves archive and counts the requests.
func serveRelease(t *testing.T, archive []byte) (*httptest.Server, *int) {
	t.Helper()
	hits := new(int)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*hits++
		w.Write(archive)
	}))
	t.Cleanup(srv.Close)
	return srv, hits
}

func testRelease(srv *httptest.Server, archive, binary []byte) release {
	return release{version: "9.9.9", url: srv.URL + "/v9.9.9/rclone.zip", member: testMember, zipSHA256: sum(archive), binarySHA256: sum(binary)}
}

// leftovers lists what is in the rclone directory besides keep.
func leftovers(t *testing.T, d sysexec.Deps, keep string) []string {
	t.Helper()
	entries, _ := os.ReadDir(d.Paths.RcloneDir())
	var out []string
	for _, e := range entries {
		if d.Paths.RcloneDir()+"/"+e.Name() != keep {
			out = append(out, e.Name())
		}
	}
	return out
}

func TestEnsureInstallsTheBinaryAndReusesIt(t *testing.T) {
	binary := []byte("#!/bin/true rclone")
	archive := zipOf(t, testMember, binary)
	srv, hits := serveRelease(t, archive)
	d := testDeps(t, srv)
	r := testRelease(srv, archive, binary)
	for range 2 {
		path, err := ensure(context.Background(), d, r)
		if err != nil {
			t.Fatal(err)
		}
		if path != d.Paths.RcloneBinary("9.9.9") {
			t.Errorf("path = %q", path)
		}
	}
	got, _ := os.ReadFile(d.Paths.RcloneBinary("9.9.9"))
	fi, _ := os.Stat(d.Paths.RcloneBinary("9.9.9"))
	if !bytes.Equal(got, binary) || fi.Mode().Perm() != 0o755 || *hits != 1 {
		t.Errorf("binary %q mode %v after %d downloads; want the member, 0755, 1 download", got, fi.Mode().Perm(), *hits)
	}
	if l := leftovers(t, d, d.Paths.RcloneBinary("9.9.9")); len(l) != 0 {
		t.Errorf("left behind: %v", l)
	}
}

func TestEnsureRefusesWrongHash(t *testing.T) {
	binary := []byte("rclone")
	archive := zipOf(t, testMember, binary)
	srv, _ := serveRelease(t, archive)
	d := testDeps(t, srv)
	r := testRelease(srv, archive, binary)
	r.zipSHA256 = sum([]byte("another zip"))
	if _, err := ensure(context.Background(), d, r); err == nil {
		t.Fatal("a zip with the wrong hash was accepted")
	}
	if l := leftovers(t, d, ""); len(l) != 0 {
		t.Errorf("left behind: %v", l)
	}
}

// The zip's hash is checked, then the binary's: a release whose zip matches
// but whose member doesn't is refused too.
func TestEnsureRefusesWrongBinaryHash(t *testing.T) {
	archive := zipOf(t, testMember, []byte("tampered"))
	srv, _ := serveRelease(t, archive)
	d := testDeps(t, srv)
	r := testRelease(srv, archive, []byte("rclone"))
	if _, err := ensure(context.Background(), d, r); err == nil {
		t.Fatal("a binary with the wrong hash was accepted")
	}
	if l := leftovers(t, d, ""); len(l) != 0 {
		t.Errorf("left behind: %v", l)
	}
}

func TestEnsureRefusesZipWithoutTheMember(t *testing.T) {
	binary := []byte("rclone")
	archive := zipOf(t, "elsewhere/rclone", binary)
	srv, _ := serveRelease(t, archive)
	d := testDeps(t, srv)
	if _, err := ensure(context.Background(), d, testRelease(srv, archive, binary)); err == nil {
		t.Fatal("a zip without the member was accepted")
	}
}

// A binary changed on disk is replaced before it runs (R05-D1).
func TestEnsureReplacesAChangedBinary(t *testing.T) {
	binary := []byte("rclone")
	archive := zipOf(t, testMember, binary)
	srv, hits := serveRelease(t, archive)
	d := testDeps(t, srv)
	r := testRelease(srv, archive, binary)
	path, err := ensure(context.Background(), d, r)
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(path, []byte("evil"), 0o755)
	if _, err := ensure(context.Background(), d, r); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(path); !bytes.Equal(got, binary) || *hits != 2 {
		t.Errorf("binary %q after %d downloads", got, *hits)
	}
}

func TestPinsMatchSpike(t *testing.T) {
	want := map[string][2]string{
		"amd64": {"b00f00b3266c3764413afe599d3ec316f6f43b3e0ea8a0bc1d186d2133e46df7", "62e4d3e5432f2c685f1834b8bb342058ff0611c99d048aeb44bb8d8fd611527e"},
		"arm64": {"ee169016675e23a69d77240565133ff7dbc9b33d16fa8f7053d4af39f1eaeb78", "ee07f4d157b53d7b3e4f428aa292b30c1958525eb43afa49291b9b6e988a0ea3"},
	}
	for arch, w := range want {
		r, err := pinned(arch)
		if err != nil {
			t.Fatal(err)
		}
		if r.version != "1.76.0" || r.zipSHA256 != w[0] || r.binarySHA256 != w[1] {
			t.Errorf("%s: %+v", arch, r)
		}
		if r.url != "https://downloads.rclone.org/v1.76.0/rclone-v1.76.0-linux-"+arch+".zip" || r.member != "rclone-v1.76.0-linux-"+arch+"/rclone" {
			t.Errorf("%s: url %q member %q", arch, r.url, r.member)
		}
	}
	if _, err := pinned("386"); err == nil {
		t.Error("an architecture without a pin was accepted")
	}
}

// The config check image installs the same rclone the agent pins.
func TestConfigcheckImagePinsTheSameRelease(t *testing.T) {
	b, err := os.ReadFile("../../scripts/configcheck/Dockerfile")
	if err != nil {
		t.Fatal(err)
	}
	for arg, want := range map[string]string{"RCLONE_VERSION": pinVersion, "RCLONE_AMD64_SHA256": pins["amd64"].zip, "RCLONE_ARM64_SHA256": pins["arm64"].zip} {
		if !regexp.MustCompile(`(?m)^ARG ` + arg + `=` + regexp.QuoteMeta(want) + `$`).Match(b) {
			t.Errorf("the Dockerfile has no ARG %s=%s", arg, want)
		}
	}
}

func TestObscureMatchesRcloneVector(t *testing.T) {
	iv := make([]byte, 16)
	for i := range iv {
		iv[i] = byte(i)
	}
	// rclone reveal gave zimam-go-vector for this (Task 1 spike).
	if got := Obscure("zimam-go-vector", iv); got != "AAECAwQFBgcICQoLDA0OD3v2CkRq_yfl4FdtBnCPvQ" {
		t.Errorf("Obscure = %q", got)
	}
	// rclone obscure zimam-test-vector gave this.
	if got, err := Reveal("W6Z-QaVk5s3blAKpf7hqzlNaHIoLxxNTO8HcHzB5wczC"); err != nil || got != "zimam-test-vector" {
		t.Errorf("Reveal = %q, %v", got, err)
	}
}

func TestRevealInvertsObscure(t *testing.T) {
	iv := bytes.Repeat([]byte{7}, 16)
	plain := strings.Repeat("k", 43)
	if got, err := Reveal(Obscure(plain, iv)); err != nil || got != plain {
		t.Errorf("Reveal(Obscure(x)) = %q, %v", got, err)
	}
	for _, bad := range []string{"", "short", "not base64 !!", "AAECAwQFBgcICQoLDA0ODw"} {
		if _, err := Reveal(bad); err == nil {
			t.Errorf("Reveal(%q) succeeded", bad)
		}
	}
}

// fakeBinary puts a file with content at the release's binary path, so run
// finds it without a download.
func fakeBinary(t *testing.T, d sysexec.Deps) release {
	t.Helper()
	content := []byte("rclone")
	path := d.Paths.RcloneBinary("9.9.9")
	os.MkdirAll(d.Paths.RcloneDir(), 0o755)
	if err := os.WriteFile(path, content, 0o755); err != nil {
		t.Fatal(err)
	}
	return release{version: "9.9.9", binarySHA256: sum(content), url: "https://invalid.example/never"}
}

func TestRunAlwaysPassesConfig(t *testing.T) {
	d := testDeps(t, nil)
	r := fakeBinary(t, d)
	fake := sysexec.NewFakeRunner(sysexec.Script{Path: d.Paths.RcloneBinary("9.9.9"),
		Args: []string{"--config", d.Paths.RcloneConf(), "--low-level-retries", "3", "lsf", "offsite:"}})
	d.Run = fake
	if _, err := run(context.Background(), d, r, "lsf", "offsite:"); err != nil {
		t.Fatal(err)
	}
	if calls := fake.Calls(); len(calls) != 1 {
		t.Fatalf("calls = %+v", calls)
	}
}

// rclone gets no RCLONE_* or HOME variables, so nothing but rclone.conf
// configures it (R05-D2).
func TestRunEnvironmentIsEmpty(t *testing.T) {
	d := testDeps(t, nil)
	r := fakeBinary(t, d)
	fake := sysexec.NewFakeRunner(sysexec.Script{Path: d.Paths.RcloneBinary("9.9.9")})
	d.Run = fake
	run(context.Background(), d, r, "version")
	for _, c := range fake.Calls() {
		if len(c.Env) != 0 || c.Dir != "" {
			t.Errorf("env %v dir %q", c.Env, c.Dir)
		}
	}
}

func s3Destination() Destination {
	return Destination{ID: "offsite", Type: contract.DestinationS3,
		S3: &contract.S3Settings{Endpoint: "https://s3.example.com", Region: "us-east-1", Bucket: "backups", Prefix: "zimam/prod",
			AccessKeyID: "AKIAIOSFODNN7EXAMPLE", SecretAccessKey: "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"},
		Password: Obscure(strings.Repeat("p", 43), make([]byte, 16)), Password2: Obscure(strings.Repeat("s", 43), make([]byte, 16))}
}

func sftpDestination() Destination {
	d := s3Destination()
	d.ID, d.Type, d.S3 = "box", contract.DestinationSFTP, nil
	d.SFTP = &contract.SFTPSettings{Host: "backup.example.com", Port: 23, User: "u123", Path: "zimam"}
	d.HostKeyAlgorithms = "ssh-ed25519"
	return d
}

func TestRenderConfig(t *testing.T) {
	p := paths.Paths{}
	out, err := RenderConfig(p, []Destination{sftpDestination(), s3Destination()})
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	for _, want := range []string{
		"[box-raw]\ntype = sftp\n", "key_file = /etc/zimam/backup/box.key\n", "known_hosts_file = /etc/zimam/backup/box.known_hosts\n",
		"host_key_algorithms = ssh-ed25519\n", "[box]\ntype = crypt\nremote = box-raw:zimam\n",
		"[offsite-raw]\ntype = s3\n", "[offsite]\ntype = crypt\nremote = offsite-raw:backups/zimam/prod\n",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in\n%s", want, s)
		}
	}
	if strings.Index(s, "[box-raw]") > strings.Index(s, "[offsite-raw]") {
		t.Error("destinations aren't sorted by id")
	}
}

// Until the first test pins a host key, an SFTP remote has no algorithm
// line, and rclone refuses to connect: its known_hosts file is empty.
func TestRenderConfigWithoutPinnedKey(t *testing.T) {
	d := sftpDestination()
	d.HostKeyAlgorithms = ""
	out, err := RenderConfig(paths.Paths{}, []Destination{d})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "host_key_algorithms") || !strings.Contains(string(out), "known_hosts_file") {
		t.Errorf("got\n%s", out)
	}
}

func TestRenderConfigRefuses(t *testing.T) {
	mod := func(base func() Destination, f func(*Destination)) []Destination {
		d := base()
		f(&d)
		return []Destination{d}
	}
	for name, dests := range map[string][]Destination{
		"duplicate id":       {s3Destination(), s3Destination()},
		"bad id":             mod(s3Destination, func(d *Destination) { d.ID = "X" }),
		"s3 without fields":  mod(s3Destination, func(d *Destination) { d.S3 = nil }),
		"sftp without":       mod(sftpDestination, func(d *Destination) { d.SFTP = nil }),
		"bad bucket":         mod(s3Destination, func(d *Destination) { d.S3.Bucket = "a\nb" }),
		"unknown algorithm":  mod(sftpDestination, func(d *Destination) { d.HostKeyAlgorithms = "ssh-dss" }),
		"algorithm for s3":   mod(s3Destination, func(d *Destination) { d.HostKeyAlgorithms = "ssh-ed25519" }),
		"plain password":     mod(s3Destination, func(d *Destination) { d.Password = "secret" }),
		"missing password2":  mod(s3Destination, func(d *Destination) { d.Password2 = "" }),
		"newline in obscure": mod(s3Destination, func(d *Destination) { d.Password += "\n[x]" }),
	} {
		if _, err := RenderConfig(paths.Paths{}, dests); err == nil {
			t.Errorf("%s: rendered", name)
		}
	}
}

func TestHostKeyAlgorithms(t *testing.T) {
	for _, a := range []string{"ssh-ed25519", "ecdsa-sha2-nistp256", "ecdsa-sha2-nistp384", "ecdsa-sha2-nistp521", "rsa-sha2-512 rsa-sha2-256"} {
		if !slices.Contains(HostKeyAlgorithms, a) {
			t.Errorf("%q is missing", a)
		}
	}
}
