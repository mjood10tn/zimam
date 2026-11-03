package metrics

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"

	"zimam/agent/contract"
	"zimam/agent/internal/sysexec"
)

// procFiles places the captured /proc files where Paths finds them.
func procFiles(t *testing.T, d sysexec.Deps) {
	t.Helper()
	for name, to := range map[string]string{
		"uptime":  d.Paths.ProcUptime(),
		"loadavg": d.Paths.ProcLoadavg(),
		"stat":    d.Paths.ProcStat(),
		"meminfo": d.Paths.ProcMeminfo(),
		"mounts":  d.Paths.ProcMounts(),
		"net-dev": d.Paths.ProcNetDev(),
	} {
		if err := os.MkdirAll(filepath.Dir(to), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(to, fixture(t, name), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func fakeStatfs(t *testing.T) {
	t.Helper()
	old := statfs
	statfs = func(path string, st *unix.Statfs_t) error {
		*st = unix.Statfs_t{Bsize: 4096, Frsize: 4096, Blocks: 100, Bfree: 10, Bavail: 5, Files: 50, Ffree: 25}
		return nil
	}
	t.Cleanup(func() { statfs = old })
}

func TestCollect(t *testing.T) {
	fakeStatfs(t)
	units := append([]string{"nginx.service"}, alwaysUnits...)
	blocks := append([][3]string{{"nginx.service", "active", "enabled"}}, fixedBlocks()...)
	run := sysexec.NewFakeRunner(sysexec.Script{Path: "/usr/bin/systemctl", Args: showArgs(units...), Result: sysexec.Result{Stdout: show(blocks...)}})
	d := testDeps(t, run)
	procFiles(t, d)

	got, err := Collect(context.Background(), d)
	if err != nil {
		t.Fatal(err)
	}
	if got.UptimeSeconds != 1021453.67 || got.Load.Load5 != 0.31 || got.CPU.Idle != 98412377 || got.Memory.AvailableBytes != 2803112*1024 {
		t.Errorf("Collect = %+v", got)
	}
	if got.Network.RxBytes != 52310984411+102400 {
		t.Errorf("network = %+v", got.Network)
	}
	if len(got.Disks) != 3 || got.Disks[0] != (contract.DiskUsage{Mount: "/", FSType: "ext4", TotalBytes: 409600, UsedBytes: 90 * 4096, AvailableBytes: 5 * 4096, InodesTotal: 50, InodesFree: 25}) {
		t.Errorf("disks = %+v", got.Disks)
	}
	if len(got.Services) != 7 || got.Services[6].Unit != "ssh.socket" {
		t.Errorf("services = %+v", got.Services)
	}
}

// A missing /proc file fails the call rather than reporting zeros, which
// the panel would chart as a real value.
func TestCollectMissingProcFile(t *testing.T) {
	fakeStatfs(t)
	d := testDeps(t, sysexec.NewFakeRunner())
	procFiles(t, d)
	if err := os.Remove(d.Paths.ProcMeminfo()); err != nil {
		t.Fatal(err)
	}
	if _, err := Collect(context.Background(), d); err == nil || !strings.Contains(err.Error(), "meminfo") {
		t.Errorf("Collect = %v, want an error naming meminfo", err)
	}
}

// Empty lists are [] on the wire, never null: the schema says array.
func TestCollectEmptyListsAreArrays(t *testing.T) {
	old := statfs
	statfs = func(string, *unix.Statfs_t) error { return unix.EACCES }
	t.Cleanup(func() { statfs = old })
	units := append([]string{"nginx.service"}, alwaysUnits...)
	blocks := append([][3]string{{"nginx.service", "active", "enabled"}}, fixedBlocks()...)
	blocks[len(blocks)-2][2], blocks[len(blocks)-1][2] = "disabled", "disabled"
	run := sysexec.NewFakeRunner(sysexec.Script{Path: "/usr/bin/systemctl", Args: showArgs(units...), Result: sysexec.Result{Stdout: show(blocks...)}})
	d := testDeps(t, run)
	procFiles(t, d)
	got, err := Collect(context.Background(), d)
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"disks":[]`) {
		t.Errorf("no readable mount gives %s, want \"disks\":[]", b)
	}
}
