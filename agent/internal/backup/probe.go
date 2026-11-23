package backup

import (
	"context"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"

	"zimam/agent/contract"
	"zimam/agent/internal/configtx"
	"zimam/agent/internal/sysexec"
)

// probeDir is where a test writes its probe, on the crypt remote.
const probeDir = "zimam-probe"

// TestDestination writes, lists and deletes a probe object through the
// destination's crypt remote (R05 Task 4). An SFTP destination without a
// pinned host key gets one first: the key is pinned only if the probe then
// passes (OQ3). A pinned key is trusted as it is, so a server whose key
// changed fails here.
func TestDestination(ctx context.Context, d sysexec.Deps, id contract.DestinationID) (contract.BackupDestinationTestResult, error) {
	res := contract.BackupDestinationTestResult{ID: id}
	ds, err := loadDestinations(d.Paths)
	if err != nil {
		return res, err
	}
	i := find(ds, id)
	if i < 0 {
		return res, notFound(id)
	}
	dest := ds[i]
	if dest.SFTP == nil || dest.PinnedFingerprint != "" {
		res.HostKeyFingerprint = string(dest.PinnedFingerprint)
		return res, probe(ctx, d, id)
	}
	keys, err := scanHostKeys(ctx, d, dest.SFTP.Host, dest.SFTP.Port)
	if err != nil {
		return res, err
	}
	k, err := pickHostKey(keys, dest.SFTP.HostKeyFingerprint)
	if err != nil {
		return res, err
	}
	sec, err := loadSecrets(d.Paths)
	if err != nil {
		return res, err
	}
	ds[i].PinnedFingerprint, ds[i].HostKeyAlgorithms = k.fingerprint, k.algorithms
	d.Logf("Trying host key %s of %s", k.fingerprint, dest.SFTP.Host)
	pin := configtx.RootFile(d.Paths.BackupKnownHosts(id), []byte(k.knownHosts(dest.SFTP.Host, dest.SFTP.Port)), 0o600)
	if err := writeConfig(ctx, d, ds, sec, []configtx.Change{pin}, func(ctx context.Context) error { return probe(ctx, d, id) }); err != nil {
		return res, err
	}
	d.Logf("Pinned host key %s", k.fingerprint)
	res.HostKeyFingerprint = string(k.fingerprint)
	return res, saveDestinations(d.Paths, ds)
}

// probe writes a small object, finds it in a listing and deletes it.
func probe(ctx context.Context, d sysexec.Deps, id contract.DestinationID) error {
	name := "probe-" + strconv.FormatInt(d.Now().UnixNano(), 10)
	dir := string(id) + ":" + probeDir
	step := func(what string, stdin io.Reader, args ...string) (sysexec.Result, error) {
		res, err := rcloneRun(ctx, d, stdin, args...)
		if err != nil {
			return res, fmt.Errorf("%s failed: %w", what, err)
		}
		return res, nil
	}
	if _, err := step("writing the probe", strings.NewReader("zimam backup destination test\n"), "rcat", dir+"/"+name); err != nil {
		return err
	}
	res, err := step("listing the probe", nil, "lsf", dir)
	if err != nil {
		return err
	}
	if !slices.Contains(strings.Split(strings.TrimSpace(string(res.Stdout)), "\n"), name) {
		return fmt.Errorf("the probe %s was written but isn't listed", name)
	}
	if _, err := step("deleting the probe", nil, "deletefile", dir+"/"+name); err != nil {
		return err
	}
	rcloneRun(ctx, d, nil, "rmdir", dir) // tidy only: other probes may still be there
	d.Logf("Wrote, listed and deleted a probe on %s", id)
	return nil
}
