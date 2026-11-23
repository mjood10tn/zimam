package backup

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"zimam/agent/contract"
	"zimam/agent/internal/paths"
	"zimam/agent/internal/rclone"
	"zimam/agent/internal/sysexec"
)

var (
	testPassword = contract.EncryptionSecret(strings.Repeat("p", 43))
	testSalt     = contract.EncryptionSecret(strings.Repeat("s", 43))
	testAccess   = contract.S3Credential("AKIAIOSFODNN7EXAMPLE")
	testSecret   = contract.S3Credential("wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY")
)

// fakeRclone records rclone runs and answers them from answer.
type fakeRclone struct {
	mu     sync.Mutex
	calls  [][]string
	stdins []string
	answer func(args []string) (sysexec.Result, error)
}

func (f *fakeRclone) run(_ context.Context, _ sysexec.Deps, stdin io.Reader, args ...string) (sysexec.Result, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	in := ""
	if stdin != nil {
		b, _ := io.ReadAll(stdin)
		in = string(b)
	}
	f.calls = append(f.calls, slices.Clone(args))
	f.stdins = append(f.stdins, in)
	if f.answer != nil {
		return f.answer(args)
	}
	return sysexec.Result{}, nil
}

// commands lists the rclone subcommands run, in order.
func (f *fakeRclone) commands() []string {
	var out []string
	for _, c := range f.calls {
		out = append(out, c[0])
	}
	return out
}

// probeAnswer is a destination that takes the probe: lsf lists what rcat
// wrote.
func probeAnswer(f *fakeRclone) func([]string) (sysexec.Result, error) {
	return func(args []string) (sysexec.Result, error) {
		if args[0] == "lsf" {
			for _, c := range f.calls {
				if c[0] == "rcat" {
					return sysexec.Result{Stdout: []byte(filepath.Base(c[1]) + "\n")}, nil
				}
			}
		}
		return sysexec.Result{}, nil
	}
}

// keygenRunner makes the files ssh-keygen would, then answers as fake does.
type keygenRunner struct {
	*sysexec.FakeRunner
}

func (k keygenRunner) Run(ctx context.Context, c sysexec.Cmd) (sysexec.Result, error) {
	if c.Path == sshKeygen {
		f := c.Args[slices.Index(c.Args, "-f")+1]
		os.WriteFile(f, []byte("PRIVATE KEY"), 0o600)
		os.WriteFile(f+".pub", []byte("ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIEXAMPLE zimam-backup\n"), 0o644)
	}
	return k.FakeRunner.Run(ctx, c)
}

// hostKey is a fake ed25519 host key blob and its keyscan line.
func hostKey(seed string) (line string, fingerprint contract.HostKeyFingerprint) {
	typ := "ssh-ed25519"
	blob := append([]byte{0, 0, 0, byte(len(typ))}, typ...)
	blob = append(blob, 0, 0, 0, 32)
	key := sha256.Sum256([]byte(seed))
	blob = append(blob, key[:]...)
	sum := sha256.Sum256(blob)
	return "backup.example.com " + typ + " " + base64.StdEncoding.EncodeToString(blob) + "\n",
		contract.HostKeyFingerprint("SHA256:" + base64.RawStdEncoding.EncodeToString(sum[:]))
}

func keyscanScript(stdout string) sysexec.Script {
	return sysexec.Script{Path: sshKeyscan, Args: []string{"-T", "10", "-p", "22", "-t", "ed25519,ecdsa,rsa", "backup.example.com"},
		Result: sysexec.Result{Stdout: []byte(stdout)}}
}

type env struct {
	d      sysexec.Deps
	fake   *sysexec.FakeRunner
	rclone *fakeRclone
	logs   *strings.Builder
}

func newEnv(t *testing.T, scripts ...sysexec.Script) env {
	t.Helper()
	p := paths.Paths{Root: t.TempDir()}
	os.MkdirAll(p.LocksDir(), 0o700)
	keygen := sysexec.Script{Path: sshKeygen, Args: []string{"-q", "-t", "ed25519", "-N", "", "-C", "zimam-backup-box", "-f", p.BackupSFTPKey("box")}}
	fake := sysexec.NewFakeRunner(append(scripts, keygen)...)
	logs := &strings.Builder{}
	d := sysexec.Deps{Paths: p, Run: keygenRunner{fake}, Now: func() time.Time { return time.Date(2026, 11, 23, 3, 30, 0, 0, time.UTC) },
		Logf: func(f string, a ...any) { fmt.Fprintf(logs, f+"\n", a...) }}
	f := &fakeRclone{}
	old := rcloneRun
	rcloneRun = f.run
	t.Cleanup(func() { rcloneRun = old })
	return env{d: d, fake: fake, rclone: f, logs: logs}
}

func s3Params() *contract.BackupDestinationParams {
	return &contract.BackupDestinationParams{ID: "offsite", Type: contract.DestinationS3,
		S3: &contract.S3Settings{Endpoint: "https://s3.example.com", Region: "us-east-1", Bucket: "backups", Prefix: "zimam",
			AccessKeyID: testAccess, SecretAccessKey: testSecret},
		EncryptionPassword: testPassword, EncryptionSalt: testSalt}
}

func sftpParams() *contract.BackupDestinationParams {
	return &contract.BackupDestinationParams{ID: "box", Type: contract.DestinationSFTP,
		SFTP:               &contract.SFTPSettings{Host: "backup.example.com", Port: 22, User: "backup", Path: "zimam"},
		EncryptionPassword: testPassword, EncryptionSalt: testSalt}
}

func readConf(t *testing.T, e env) string {
	t.Helper()
	b, err := os.ReadFile(e.d.Paths.RcloneConf())
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestDestinationRendersCryptOverRaw(t *testing.T) {
	e := newEnv(t)
	if _, err := SetDestination(context.Background(), e.d, s3Params()); err != nil {
		t.Fatal(err)
	}
	conf := readConf(t, e)
	for _, want := range []string{"[offsite-raw]\ntype = s3\n", "[offsite]\ntype = crypt\nremote = offsite-raw:backups/zimam\n"} {
		if !strings.Contains(conf, want) {
			t.Errorf("rclone.conf lacks %q:\n%s", want, conf)
		}
	}
	fi, _ := os.Stat(e.d.Paths.RcloneConf())
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("rclone.conf mode %v", fi.Mode().Perm())
	}
	// The config passed rclone's own parse before it stayed (R05-D2).
	if got := e.rclone.calls; len(got) != 1 || !slices.Equal(got[0], []string{"config", "show", "offsite"}) {
		t.Errorf("rclone calls = %v", got)
	}
	keys, err := DestinationKeys(e.d.Paths, "offsite")
	if err != nil || keys.EncryptionPassword != testPassword || keys.EncryptionSalt != testSalt {
		t.Errorf("keys = %+v, %v", keys, err)
	}
}

// A failed parse restores the previous rclone.conf and saves no state.
func TestDestinationRefusedByRcloneLeavesNothing(t *testing.T) {
	e := newEnv(t)
	e.rclone.answer = func([]string) (sysexec.Result, error) {
		return sysexec.Result{ExitCode: 1}, &sysexec.ExitError{Path: "rclone", Code: 1}
	}
	if _, err := SetDestination(context.Background(), e.d, s3Params()); err == nil {
		t.Fatal("accepted a config rclone refused")
	}
	for _, p := range []string{e.d.Paths.RcloneConf(), e.d.Paths.BackupDestinationsState()} {
		if _, err := os.Stat(p); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s exists: %v", p, err)
		}
	}
}

// Credentials and keys live only in rclone.conf: never in the state file,
// the job log or an argv (R05-D3, global rule 6).
func TestSecretsOnlyInRcloneConf(t *testing.T) {
	e := newEnv(t, keyscanScript(""))
	for _, p := range []*contract.BackupDestinationParams{s3Params(), sftpParams()} {
		if _, err := SetDestination(context.Background(), e.d, p); err != nil {
			t.Fatal(err)
		}
	}
	state, err := os.ReadFile(e.d.Paths.BackupDestinationsState())
	if err != nil {
		t.Fatal(err)
	}
	var argv []string
	for _, c := range e.rclone.calls {
		argv = append(argv, c...)
	}
	for _, c := range e.fake.Calls() {
		argv = append(argv, c.Args...)
	}
	for _, secret := range []string{string(testPassword), string(testSalt), string(testAccess), string(testSecret)} {
		for where, text := range map[string]string{"state": string(state), "job log": e.logs.String(), "argv": strings.Join(argv, " ")} {
			if strings.Contains(text, secret) {
				t.Errorf("the %s holds %q", where, secret)
			}
		}
	}
	conf := readConf(t, e)
	if !strings.Contains(conf, string(testAccess)) || strings.Contains(conf, string(testPassword)) {
		t.Error("rclone.conf should hold the S3 keys and only the obscured crypt keys")
	}
	fi, _ := os.Stat(e.d.Paths.BackupDestinationsState())
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("state mode %v", fi.Mode().Perm())
	}
}

func TestNewDestinationNeedsKeys(t *testing.T) {
	e := newEnv(t)
	p := s3Params()
	p.EncryptionPassword, p.EncryptionSalt = "", ""
	_, err := SetDestination(context.Background(), e.d, p)
	var ae *contract.AgentError
	if !errors.As(err, &ae) || ae.Code != contract.ErrValidationFailed {
		t.Errorf("err = %v, want validation_failed", err)
	}
}

// An edit leaves the keys out and keeps them; different keys are refused,
// since the destination's backups need the old ones (R05-D3).
func TestEditKeepsKeysAndRefusesNewOnes(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	if _, err := SetDestination(ctx, e.d, s3Params()); err != nil {
		t.Fatal(err)
	}
	edit := s3Params()
	edit.EncryptionPassword, edit.EncryptionSalt = "", ""
	edit.S3.Bucket = "other-bucket"
	if _, err := SetDestination(ctx, e.d, edit); err != nil {
		t.Fatal(err)
	}
	if keys, _ := DestinationKeys(e.d.Paths, "offsite"); keys.EncryptionPassword != testPassword {
		t.Errorf("keys after edit = %+v", keys)
	}
	if !strings.Contains(readConf(t, e), "remote = offsite-raw:other-bucket/zimam\n") {
		t.Error("the edit didn't reach rclone.conf")
	}
	// The same keys again (a retried job) are fine.
	if _, err := SetDestination(ctx, e.d, s3Params()); err != nil {
		t.Errorf("same keys: %v", err)
	}
	other := s3Params()
	other.EncryptionPassword = contract.EncryptionSecret(strings.Repeat("q", 43))
	_, err := SetDestination(ctx, e.d, other)
	var ae *contract.AgentError
	if !errors.As(err, &ae) || ae.Code != contract.ErrConflict {
		t.Errorf("new keys: err = %v, want conflict", err)
	}
}

func TestSFTPKeyGeneratedOnce(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	res, err := SetDestination(ctx, e.d, sftpParams())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(res.PublicKey, "ssh-ed25519 ") {
		t.Errorf("public key = %q", res.PublicKey)
	}
	again, err := SetDestination(ctx, e.d, sftpParams())
	if err != nil || again.PublicKey != res.PublicKey {
		t.Errorf("second run: %+v, %v", again, err)
	}
	n := 0
	for _, c := range e.fake.Calls() {
		if c.Path == sshKeygen {
			n++
			if !slices.Equal(c.Args, []string{"-q", "-t", "ed25519", "-N", "", "-C", "zimam-backup-box", "-f", e.d.Paths.BackupSFTPKey("box")}) {
				t.Errorf("ssh-keygen %q", c.Args)
			}
		}
	}
	if n != 1 {
		t.Errorf("ssh-keygen ran %d times", n)
	}
	conf := readConf(t, e)
	if !strings.Contains(conf, "known_hosts_file = "+e.d.Paths.BackupKnownHosts("box")) || strings.Contains(conf, "host_key_algorithms") {
		t.Errorf("an untested SFTP destination must have an empty known_hosts and no algorithm:\n%s", conf)
	}
	if b, err := os.ReadFile(e.d.Paths.BackupKnownHosts("box")); err != nil || len(b) != 0 {
		t.Errorf("known_hosts = %q, %v", b, err)
	}
}

func TestTestWritesListsDeletes(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	if _, err := SetDestination(ctx, e.d, s3Params()); err != nil {
		t.Fatal(err)
	}
	e.rclone.calls = nil
	e.rclone.answer = probeAnswer(e.rclone)
	if _, err := TestDestination(ctx, e.d, "offsite"); err != nil {
		t.Fatal(err)
	}
	if got := e.rclone.commands(); !slices.Equal(got, []string{"rcat", "lsf", "deletefile", "rmdir"}) {
		t.Errorf("rclone ran %v", got)
	}
	if obj := e.rclone.calls[0][1]; !strings.HasPrefix(obj, "offsite:zimam-probe/") {
		t.Errorf("probe object %q isn't on the crypt remote", obj)
	}
}

func TestTestFailsWhenTheProbeIsntListed(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	SetDestination(ctx, e.d, s3Params())
	if _, err := TestDestination(ctx, e.d, "offsite"); err == nil {
		t.Fatal("passed although lsf didn't list the probe")
	}
}

func TestTestPinsTheHostKey(t *testing.T) {
	line, fp := hostKey("server")
	e := newEnv(t, keyscanScript("# comment\n"+line))
	ctx := context.Background()
	SetDestination(ctx, e.d, sftpParams())
	e.rclone.answer = probeAnswer(e.rclone)
	res, err := TestDestination(ctx, e.d, "box")
	if err != nil {
		t.Fatal(err)
	}
	if res.HostKeyFingerprint != string(fp) {
		t.Errorf("fingerprint %q, want %q", res.HostKeyFingerprint, fp)
	}
	if b, _ := os.ReadFile(e.d.Paths.BackupKnownHosts("box")); string(b) != line {
		t.Errorf("known_hosts = %q", b)
	}
	if !strings.Contains(readConf(t, e), "host_key_algorithms = ssh-ed25519\n") {
		t.Error("rclone.conf has no algorithm")
	}
	// Once pinned, a test doesn't scan again.
	if _, err := TestDestination(ctx, e.d, "box"); err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, c := range e.fake.Calls() {
		if c.Path == sshKeyscan {
			n++
		}
	}
	if n != 1 {
		t.Errorf("keyscan ran %d times", n)
	}
}

// A probe that fails leaves the host key unpinned (OQ3: pinned on the
// first successful test).
func TestFailedProbeDoesntPin(t *testing.T) {
	line, _ := hostKey("server")
	e := newEnv(t, keyscanScript(line))
	ctx := context.Background()
	SetDestination(ctx, e.d, sftpParams())
	if _, err := TestDestination(ctx, e.d, "box"); err == nil {
		t.Fatal("passed")
	}
	if b, _ := os.ReadFile(e.d.Paths.BackupKnownHosts("box")); len(b) != 0 {
		t.Errorf("known_hosts = %q after a failed test", b)
	}
	if strings.Contains(readConf(t, e), "host_key_algorithms") {
		t.Error("rclone.conf kept the algorithm")
	}
}

func TestGivenFingerprintMismatchFailsTest(t *testing.T) {
	line, _ := hostKey("server")
	_, other := hostKey("someone else")
	e := newEnv(t, keyscanScript(line))
	ctx := context.Background()
	p := sftpParams()
	p.SFTP.HostKeyFingerprint = other
	SetDestination(ctx, e.d, p)
	e.rclone.answer = probeAnswer(e.rclone)
	_, err := TestDestination(ctx, e.d, "box")
	if err == nil || !strings.Contains(err.Error(), string(other)) {
		t.Fatalf("err = %v, want a mismatch naming the expected fingerprint", err)
	}
	if len(e.rclone.calls) != 1 { // only SetDestination's config show
		t.Errorf("rclone ran after a mismatch: %v", e.rclone.calls)
	}
}

// A pinned key is trusted as it is: a server whose key changed fails the
// test through rclone, and the pin stays.
func TestChangedHostKeyFailsTest(t *testing.T) {
	line, fp := hostKey("server")
	e := newEnv(t, keyscanScript(line))
	ctx := context.Background()
	SetDestination(ctx, e.d, sftpParams())
	e.rclone.answer = probeAnswer(e.rclone)
	if _, err := TestDestination(ctx, e.d, "box"); err != nil {
		t.Fatal(err)
	}
	e.rclone.answer = func([]string) (sysexec.Result, error) {
		return sysexec.Result{ExitCode: 1, Stderr: []byte("knownhosts: key mismatch")}, &sysexec.ExitError{Path: "rclone", Code: 1, Stderr: []byte("knownhosts: key mismatch")}
	}
	_, err := TestDestination(ctx, e.d, "box")
	if err == nil || !strings.Contains(err.Error(), "key mismatch") {
		t.Fatalf("err = %v", err)
	}
	if b, _ := os.ReadFile(e.d.Paths.BackupKnownHosts("box")); string(b) != line {
		t.Errorf("the pin changed: %q", b)
	}
	ds, _ := loadDestinations(e.d.Paths)
	if ds[0].PinnedFingerprint != fp {
		t.Errorf("state pin = %q", ds[0].PinnedFingerprint)
	}
}

// An empty scan is an error, never "no keys": sshd's PerSourcePenalties
// can refuse repeated scans (Task 1 spike).
func TestEmptyKeyscanFails(t *testing.T) {
	e := newEnv(t, keyscanScript(""))
	ctx := context.Background()
	SetDestination(ctx, e.d, sftpParams())
	if _, err := TestDestination(ctx, e.d, "box"); err == nil || !strings.Contains(err.Error(), "no host key") {
		t.Fatalf("err = %v", err)
	}
}

func TestPickHostKey(t *testing.T) {
	ed, edFP := hostKey("ed")
	rsaBlob := append([]byte{0, 0, 0, 7}, "ssh-rsa"...)
	rsaBlob = append(rsaBlob, 1, 2, 3)
	rsa := "backup.example.com ssh-rsa " + base64.StdEncoding.EncodeToString(rsaBlob) + "\n"
	keys, err := parseKeyscan([]byte(rsa + ed))
	if err != nil || len(keys) != 2 {
		t.Fatalf("keys %v, %v", keys, err)
	}
	k, err := pickHostKey(keys, "")
	if err != nil || k.fingerprint != edFP || k.algorithms != "ssh-ed25519" {
		t.Errorf("preferred = %+v, %v", k, err)
	}
	rsaKey, _ := pickHostKey(keys[:1], "")
	if rsaKey.algorithms != "rsa-sha2-512 rsa-sha2-256" {
		t.Errorf("rsa algorithms %q", rsaKey.algorithms)
	}
	if k, err := pickHostKey(keys, rsaKey.fingerprint); err != nil || k.typ != "ssh-rsa" {
		t.Errorf("the owner's fingerprint picks its key: %+v, %v", k, err)
	}
	for _, bad := range []string{"backup.example.com ssh-dss AAAA\n", "backup.example.com ssh-ed25519 !!!\n",
		"backup.example.com ssh-ed25519 " + base64.StdEncoding.EncodeToString(rsaBlob) + "\n"} {
		if keys, _ := parseKeyscan([]byte(bad)); len(keys) != 0 {
			t.Errorf("parsed %q as %v", bad, keys)
		}
	}
}

func TestKnownHostsLineNamesThePort(t *testing.T) {
	k := hostKeyLine{typ: "ssh-ed25519", blob: "AAAA"}
	if got := k.knownHosts("backup.example.com", 22); got != "backup.example.com ssh-ed25519 AAAA\n" {
		t.Errorf("port 22: %q", got)
	}
	if got := k.knownHosts("backup.example.com", 23); got != "[backup.example.com]:23 ssh-ed25519 AAAA\n" {
		t.Errorf("port 23: %q", got)
	}
}

func TestDeleteRefusedWhilePlanUsesIt(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	SetDestination(ctx, e.d, s3Params())
	os.MkdirAll(e.d.Paths.BackupStateDir(), 0o700)
	os.WriteFile(e.d.Paths.BackupPlansState(), []byte(`{"plans":[{"id":"nightly","destination":"offsite","panel":true,"schedule":"hourly","retention":{"keep_last":1}}]}`), 0o600)
	_, err := DeleteDestination(ctx, e.d, "offsite")
	var ae *contract.AgentError
	if !errors.As(err, &ae) || ae.Code != contract.ErrConflict || !strings.Contains(ae.Message, "nightly") {
		t.Fatalf("err = %v, want conflict naming the plan", err)
	}
	if !strings.Contains(readConf(t, e), "[offsite]") {
		t.Error("the destination was removed")
	}
}

func TestDeleteRemovesTheDestinationAndItsFiles(t *testing.T) {
	line, _ := hostKey("server")
	e := newEnv(t, keyscanScript(line))
	ctx := context.Background()
	SetDestination(ctx, e.d, s3Params())
	SetDestination(ctx, e.d, sftpParams())
	if _, err := DeleteDestination(ctx, e.d, "box"); err != nil {
		t.Fatal(err)
	}
	conf := readConf(t, e)
	if strings.Contains(conf, "[box") || !strings.Contains(conf, "[offsite]") {
		t.Errorf("rclone.conf:\n%s", conf)
	}
	for _, p := range []string{e.d.Paths.BackupSFTPKey("box"), e.d.Paths.BackupSFTPKey("box") + ".pub", e.d.Paths.BackupKnownHosts("box")} {
		if _, err := os.Stat(p); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s still exists", p)
		}
	}
	if keys, err := DestinationKeys(e.d.Paths, "offsite"); err != nil || keys.EncryptionPassword != testPassword {
		t.Errorf("the other destination lost its keys: %+v, %v", keys, err)
	}
	_, err := DeleteDestination(ctx, e.d, "box")
	var ae *contract.AgentError
	if !errors.As(err, &ae) || ae.Code != contract.ErrNotFound {
		t.Errorf("second delete: %v", err)
	}
}

func TestKeysReadReturnsSubmittedKeys(t *testing.T) {
	e := newEnv(t)
	SetDestination(context.Background(), e.d, s3Params())
	keys, err := DestinationKeys(e.d.Paths, "offsite")
	if err != nil || keys != (contract.BackupDestinationKeysResult{ID: "offsite", EncryptionPassword: testPassword, EncryptionSalt: testSalt}) {
		t.Errorf("keys = %+v, %v", keys, err)
	}
	_, err = DestinationKeys(e.d.Paths, "nope")
	var ae *contract.AgentError
	if !errors.As(err, &ae) || ae.Code != contract.ErrNotFound {
		t.Errorf("unknown id: %v", err)
	}
}

// What the agent obscured, rclone can reveal (Task 1's algorithm).
func TestObscuredKeysAreRclones(t *testing.T) {
	e := newEnv(t)
	SetDestination(context.Background(), e.d, s3Params())
	var obscured []string
	for _, l := range strings.Split(readConf(t, e), "\n") {
		if v, ok := strings.CutPrefix(l, "password = "); ok {
			obscured = append(obscured, v)
		}
	}
	if len(obscured) != 1 {
		t.Fatalf("password lines: %v", obscured)
	}
	if plain, err := rclone.Reveal(obscured[0]); err != nil || plain != string(testPassword) {
		t.Errorf("reveal = %q, %v", plain, err)
	}
}
