package metrics

import (
	"errors"
	"reflect"
	"testing"

	"golang.org/x/sys/unix"

	"zimam/agent/contract"
)

// Only real filesystems are reported: no proc, sysfs, tmpfs, cgroup and the
// like, and no snaps' squashfs images, which are always 100% full. Escaped
// spaces in mount points are decoded.
func TestMountsSkipPseudo(t *testing.T) {
	got := ParseMounts(fixture(t, "mounts"))
	want := []Mount{
		{Point: "/", FSType: "ext4"},
		{Point: "/boot/efi", FSType: "vfat"},
		{Point: "/srv/data disk", FSType: "xfs"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ParseMounts =\n%+v\nwant\n%+v", got, want)
	}
}

// A mount point mounted twice (an over-mount) is reported once; a line too
// short to read is skipped.
func TestMountsOnceEach(t *testing.T) {
	in := "/dev/sda1 / ext4 rw 0 0\n/dev/sdb1 /srv xfs rw 0 0\nbroken\n/dev/sdc1 /srv ext4 rw 0 0\n"
	want := []Mount{{Point: "/", FSType: "ext4"}, {Point: "/srv", FSType: "ext4"}}
	if got := ParseMounts([]byte(in)); !reflect.DeepEqual(got, want) {
		t.Errorf("ParseMounts = %+v, want %+v", got, want)
	}
}

func TestUnescapeMountPoint(t *testing.T) {
	for in, want := range map[string]string{
		`/srv/a\040b`:  "/srv/a b",
		`/srv/tab\011`: "/srv/tab\t",
		`/srv/nl\012x`: "/srv/nl\nx",
		`/srv/bs\134`:  `/srv/bs\`,
		`/srv/odd\04`:  `/srv/odd\04`, // not three octal digits: kept as written
		`/srv/x\999`:   `/srv/x\999`,
	} {
		if got := unescapeMount(in); got != want {
			t.Errorf("unescapeMount(%q) = %q, want %q", in, got, want)
		}
	}
}

// Sizes come from statfs as df computes them; a mount statfs can't read is
// skipped, so one bad mount doesn't blank the others.
func TestDiskUsageFromStatfs(t *testing.T) {
	fake := func(path string, st *unix.Statfs_t) error {
		switch path {
		case "/":
			*st = unix.Statfs_t{Bsize: 4096, Frsize: 4096, Blocks: 1000, Bfree: 300, Bavail: 250, Files: 500, Ffree: 100}
		case "/boot/efi":
			*st = unix.Statfs_t{Bsize: 512, Frsize: 512, Blocks: 200, Bfree: 200, Bavail: 200}
		default:
			return errors.New("permission denied")
		}
		return nil
	}
	mounts := []Mount{{Point: "/", FSType: "ext4"}, {Point: "/gone", FSType: "xfs"}, {Point: "/boot/efi", FSType: "vfat"}}
	var logged []string
	got := diskUsage(mounts, fake, func(f string, a ...any) { logged = append(logged, f) })
	want := []contract.DiskUsage{
		{Mount: "/", FSType: "ext4", TotalBytes: 4096000, UsedBytes: 700 * 4096, AvailableBytes: 250 * 4096, InodesTotal: 500, InodesFree: 100},
		{Mount: "/boot/efi", FSType: "vfat", TotalBytes: 102400, UsedBytes: 0, AvailableBytes: 102400},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("diskUsage =\n%+v\nwant\n%+v", got, want)
	}
	if len(logged) != 1 {
		t.Errorf("logged %d lines, want one for /gone", len(logged))
	}
}
