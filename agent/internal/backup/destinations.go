// Package backup holds the backup destinations, plans and runs (v0.5.0). A
// destination's secrets live only in rclone.conf (R05-D2, R05-D3); the
// state files hold what isn't secret.
package backup

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"zimam/agent/contract"
	"zimam/agent/internal/configtx"
	"zimam/agent/internal/paths"
	"zimam/agent/internal/rclone"
	"zimam/agent/internal/sysexec"
)

// rcloneRun runs the pinned rclone; tests replace it.
var rcloneRun = rclone.RunInput

const sshKeygen = "/usr/bin/ssh-keygen"

// S3Location is an S3 destination's fields without its credentials.
type S3Location struct {
	Endpoint contract.S3Endpoint `json:"endpoint"`
	Region   contract.S3Region   `json:"region"`
	Bucket   contract.S3Bucket   `json:"bucket"`
	Prefix   contract.BackupPath `json:"prefix,omitempty"`
}

// Destination is one destination's non-secret state. SFTP.HostKeyFingerprint
// is the owner's, when given; PinnedFingerprint is the key the first passed
// test pinned, whose algorithms rclone.conf names (OQ3).
type Destination struct {
	ID                contract.DestinationID      `json:"id"`
	Type              contract.DestinationType    `json:"type"`
	S3                *S3Location                 `json:"s3,omitempty"`
	SFTP              *contract.SFTPSettings      `json:"sftp,omitempty"`
	PinnedFingerprint contract.HostKeyFingerprint `json:"pinned_fingerprint,omitempty"`
	HostKeyAlgorithms string                      `json:"host_key_algorithms,omitempty"`
}

type destinationsFile struct {
	Destinations []Destination `json:"destinations"`
}

// loadDestinations reads the state strictly. A missing file is none.
func loadDestinations(p paths.Paths) ([]Destination, error) {
	var f destinationsFile
	if err := loadJSON(p.BackupDestinationsState(), &f); err != nil {
		return nil, err
	}
	return f.Destinations, nil
}

func saveDestinations(p paths.Paths, ds []Destination) error {
	return saveJSON(p, p.BackupDestinationsState(), destinationsFile{Destinations: ds})
}

// loadJSON decodes path strictly into v; a missing file leaves v as it is.
func loadJSON(path string, v any) error {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := contract.DecodeStrict(data, v); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

// saveJSON writes v to path atomically, root 0600, in the backup state
// directory (root 0700).
func saveJSON(p paths.Paths, path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(p.BackupStateDir(), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".backup-state-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // fails harmlessly once renamed
	_, err = tmp.Write(append(data, '\n'))
	if err == nil {
		err = tmp.Chmod(0o600)
	}
	if err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// secrets are what rclone.conf alone holds for a destination: the obscured
// crypt keys and, for S3, the credentials.
type secrets struct {
	password, password2  string
	accessKey, secretKey contract.S3Credential
}

// loadSecrets reads every destination's secrets back from rclone.conf, the
// file the agent rendered: "[name]" sections of "key = value" lines.
func loadSecrets(p paths.Paths) (map[contract.DestinationID]secrets, error) {
	data, err := os.ReadFile(p.RcloneConf())
	if errors.Is(err, fs.ErrNotExist) {
		return map[contract.DestinationID]secrets{}, nil
	}
	if err != nil {
		return nil, err
	}
	sections := map[string]map[string]string{}
	var cur map[string]string
	sc := bufio.NewScanner(bytes.NewReader(data))
	for sc.Scan() {
		line := sc.Text()
		switch {
		case line == "" || strings.HasPrefix(line, "#"):
		case strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]"):
			cur = map[string]string{}
			sections[line[1:len(line)-1]] = cur
		case cur != nil && strings.Contains(line, " = "):
			k, v, _ := strings.Cut(line, " = ")
			cur[k] = v
		default:
			return nil, fmt.Errorf("%s: unexpected line %q", p.RcloneConf(), line)
		}
	}
	out := map[contract.DestinationID]secrets{}
	for name, crypt := range sections {
		if crypt["type"] != "crypt" {
			continue
		}
		raw := sections[name+"-raw"]
		out[contract.DestinationID(name)] = secrets{password: crypt["password"], password2: crypt["password2"],
			accessKey: contract.S3Credential(raw["access_key_id"]), secretKey: contract.S3Credential(raw["secret_access_key"])}
	}
	return out, sc.Err()
}

// forRclone joins a destination's state and secrets as rclone.conf holds it.
func forRclone(d Destination, s secrets) rclone.Destination {
	r := rclone.Destination{ID: d.ID, Type: d.Type, SFTP: d.SFTP, HostKeyAlgorithms: d.HostKeyAlgorithms, Password: s.password, Password2: s.password2}
	if d.S3 != nil {
		r.S3 = &contract.S3Settings{Endpoint: d.S3.Endpoint, Region: d.S3.Region, Bucket: d.S3.Bucket, Prefix: d.S3.Prefix,
			AccessKeyID: s.accessKey, SecretAccessKey: s.secretKey}
	}
	return r
}

// writeConfig renders rclone.conf for ds and applies it, with extra changes,
// through configtx: test decides whether it stays (§5.5).
func writeConfig(ctx context.Context, d sysexec.Deps, ds []Destination, sec map[contract.DestinationID]secrets, extra []configtx.Change, test func(context.Context) error) error {
	var list []rclone.Destination
	for _, x := range ds {
		list = append(list, forRclone(x, sec[x.ID]))
	}
	content, err := rclone.RenderConfig(d.Paths, list)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(d.Paths.BackupConfigDir(), 0o700); err != nil {
		return err
	}
	return configtx.Apply(ctx, append([]configtx.Change{configtx.RootFile(d.Paths.RcloneConf(), content, 0o600)}, extra...), test)
}

// configShows is the config test: rclone parses the file and finds id.
func configShows(d sysexec.Deps, id contract.DestinationID) func(context.Context) error {
	return func(ctx context.Context) error {
		res, err := rcloneRun(ctx, d, nil, "config", "show", string(id))
		var exit *sysexec.ExitError
		if errors.As(err, &exit) {
			return &configtx.TestFailed{Output: strings.TrimSpace(string(res.Stderr))}
		}
		return err
	}
}

func notFound(id contract.DestinationID) error {
	return &contract.AgentError{Code: contract.ErrNotFound, Message: fmt.Sprintf("backup destination %s doesn't exist", id)}
}

func find(ds []Destination, id contract.DestinationID) int {
	return slices.IndexFunc(ds, func(x Destination) bool { return x.ID == id })
}

// obscure gives a crypt key in rclone's obscured form, with a fresh IV.
func obscure(s contract.EncryptionSecret) (string, error) {
	iv := make([]byte, 16)
	if _, err := rand.Read(iv); err != nil {
		return "", err
	}
	return rclone.Obscure(string(s), iv), nil
}

// SetDestination adds a destination or changes one's settings (R05-D2). A
// new destination takes the panel's keys; an existing one keeps its own
// (R05-D3). A new SFTP destination gets a key pair whose public half the
// owner installs; its host key is pinned by the first passed test (OQ3).
func SetDestination(ctx context.Context, d sysexec.Deps, p *contract.BackupDestinationParams) (contract.BackupDestinationResult, error) {
	res := contract.BackupDestinationResult{ID: p.ID}
	ds, err := loadDestinations(d.Paths)
	if err != nil {
		return res, err
	}
	sec, err := loadSecrets(d.Paths)
	if err != nil {
		return res, err
	}
	i := find(ds, p.ID)
	s, err := keysFor(p, i >= 0, sec[p.ID])
	if err != nil {
		return res, err
	}
	next := Destination{ID: p.ID, Type: p.Type, SFTP: p.SFTP}
	if p.S3 != nil {
		next.S3 = &S3Location{Endpoint: p.S3.Endpoint, Region: p.S3.Region, Bucket: p.S3.Bucket, Prefix: p.S3.Prefix}
		s.accessKey, s.secretKey = p.S3.AccessKeyID, p.S3.SecretAccessKey
	} else {
		s.accessKey, s.secretKey = "", ""
	}
	var extra []configtx.Change
	if p.SFTP != nil {
		// The pin stays only while it still names the same server and agrees
		// with the owner's fingerprint; otherwise the next test pins anew.
		if i >= 0 && ds[i].SFTP != nil && ds[i].SFTP.Host == p.SFTP.Host && ds[i].SFTP.Port == p.SFTP.Port &&
			(p.SFTP.HostKeyFingerprint == "" || p.SFTP.HostKeyFingerprint == ds[i].PinnedFingerprint) {
			next.PinnedFingerprint, next.HostKeyAlgorithms = ds[i].PinnedFingerprint, ds[i].HostKeyAlgorithms
		} else {
			extra = append(extra, configtx.RootFile(d.Paths.BackupKnownHosts(p.ID), nil, 0o600))
		}
		if res.PublicKey, err = ensureKeyPair(ctx, d, p.ID); err != nil {
			return res, err
		}
	} else {
		extra = append(extra, removeSFTPFiles(d.Paths, p.ID)...)
	}
	sec[p.ID] = s
	if i >= 0 {
		ds[i] = next
	} else {
		ds = append(ds, next)
	}
	if err := writeConfig(ctx, d, ds, sec, extra, configShows(d, p.ID)); err != nil {
		return res, err
	}
	d.Logf("Backup destination %s (%s) is set up", p.ID, p.Type)
	return res, saveDestinations(d.Paths, ds)
}

// keysFor returns the obscured keys a destination gets: the panel's for a
// new one, its own for an existing one. Keys given for an existing one
// must be its own, which makes a retried job harmless.
func keysFor(p *contract.BackupDestinationParams, exists bool, old secrets) (secrets, error) {
	if !exists {
		if !p.HasKeys() {
			return secrets{}, contract.FieldErrors{"encryption_password": "is required for a new destination"}.Err()
		}
		pw, err := obscure(p.EncryptionPassword)
		if err != nil {
			return secrets{}, err
		}
		pw2, err := obscure(p.EncryptionSalt)
		return secrets{password: pw, password2: pw2}, err
	}
	if p.HasKeys() {
		pw, err1 := rclone.Reveal(old.password)
		pw2, err2 := rclone.Reveal(old.password2)
		if err1 != nil || err2 != nil || pw != string(p.EncryptionPassword) || pw2 != string(p.EncryptionSalt) {
			return secrets{}, &contract.AgentError{Code: contract.ErrConflict,
				Message: fmt.Sprintf("backup destination %s keeps its encryption keys: its backups can't be read without them", p.ID)}
		}
	}
	return secrets{password: old.password, password2: old.password2}, nil
}

// ensureKeyPair makes the destination's SFTP key pair once, as root in the
// root-only backup directory, and returns its public key (R05-D2).
func ensureKeyPair(ctx context.Context, d sysexec.Deps, id contract.DestinationID) (string, error) {
	key := d.Paths.BackupSFTPKey(id)
	if _, err := os.Stat(key); errors.Is(err, fs.ErrNotExist) {
		if err := os.MkdirAll(d.Paths.BackupConfigDir(), 0o700); err != nil {
			return "", err
		}
		os.Remove(key + ".pub") // a half-made pair from a killed run
		if _, err := d.Run.Run(ctx, sysexec.Cmd{Path: sshKeygen, Args: []string{"-q", "-t", "ed25519", "-N", "", "-C", "zimam-backup-" + string(id), "-f", key}}); err != nil {
			return "", fmt.Errorf("making the SFTP key: %w", err)
		}
	} else if err != nil {
		return "", err
	}
	pub, err := os.ReadFile(key + ".pub")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(pub)), nil
}

// removeSFTPFiles are the changes that remove a destination's SFTP key pair
// and known_hosts.
func removeSFTPFiles(p paths.Paths, id contract.DestinationID) []configtx.Change {
	var out []configtx.Change
	for _, f := range []string{p.BackupSFTPKey(id), p.BackupSFTPKey(id) + ".pub", p.BackupKnownHosts(id)} {
		out = append(out, configtx.Change{Path: f, Remove: true})
	}
	return out
}

// DeleteDestination removes a destination from rclone.conf with its SFTP
// files. The backups on it stay where they are. A plan that uses it keeps
// it (R05 Task 4).
func DeleteDestination(ctx context.Context, d sysexec.Deps, id contract.DestinationID) (contract.BackupDestinationResult, error) {
	res := contract.BackupDestinationResult{ID: id}
	ds, err := loadDestinations(d.Paths)
	if err != nil {
		return res, err
	}
	i := find(ds, id)
	if i < 0 {
		return res, notFound(id)
	}
	plans, err := loadPlans(d.Paths)
	if err != nil {
		return res, err
	}
	var users []string
	for _, pl := range plans {
		if pl.Destination == id {
			users = append(users, string(pl.ID))
		}
	}
	if len(users) > 0 {
		return res, &contract.AgentError{Code: contract.ErrConflict,
			Message: fmt.Sprintf("backup destination %s is used by plan %s", id, strings.Join(users, ", "))}
	}
	sec, err := loadSecrets(d.Paths)
	if err != nil {
		return res, err
	}
	ds = slices.Delete(ds, i, i+1)
	var test func(context.Context) error
	if len(ds) > 0 {
		test = configShows(d, ds[0].ID)
	} else {
		test = func(context.Context) error { return nil } // an empty file has nothing to parse
	}
	if err := writeConfig(ctx, d, ds, sec, removeSFTPFiles(d.Paths, id), test); err != nil {
		return res, err
	}
	d.Logf("Backup destination %s is removed; its backups stay on it", id)
	return res, saveDestinations(d.Paths, ds)
}

// DestinationKeys reads a destination's crypt keys back from rclone.conf
// for "Show keys again" (OQ2).
func DestinationKeys(p paths.Paths, id contract.DestinationID) (contract.BackupDestinationKeysResult, error) {
	res := contract.BackupDestinationKeysResult{ID: id}
	ds, err := loadDestinations(p)
	if err != nil {
		return res, err
	}
	if find(ds, id) < 0 {
		return res, notFound(id)
	}
	sec, err := loadSecrets(p)
	if err != nil {
		return res, err
	}
	pw, err1 := rclone.Reveal(sec[id].password)
	pw2, err2 := rclone.Reveal(sec[id].password2)
	if err1 != nil || err2 != nil {
		return res, fmt.Errorf("rclone.conf has no readable keys for %s", id)
	}
	res.EncryptionPassword, res.EncryptionSalt = contract.EncryptionSecret(pw), contract.EncryptionSecret(pw2)
	return res, nil
}
