package metrics

import (
	"slices"
	"strings"

	"golang.org/x/sys/unix"

	"zimam/agent/contract"
)

// Mount is one line of /proc/self/mounts that names a real filesystem.
type Mount struct {
	Point  string
	FSType string
}

// pseudoFS are the filesystems that hold no files on a disk: kernel
// interfaces, memory-backed ones, and squashfs, the read-only images snaps
// mount, which are always 100% full and would always warn.
var pseudoFS = map[string]bool{
	"autofs": true, "binfmt_misc": true, "bpf": true, "cgroup": true, "cgroup2": true,
	"configfs": true, "debugfs": true, "devpts": true, "devtmpfs": true, "efivarfs": true,
	"fuse.lxcfs": true, "fuse.gvfsd-fuse": true, "fuse.portal": true, "fusectl": true,
	"hugetlbfs": true, "mqueue": true, "nsfs": true, "proc": true, "pstore": true,
	"ramfs": true, "rpc_pipefs": true, "securityfs": true, "selinuxfs": true,
	"squashfs": true, "sysfs": true, "tmpfs": true, "tracefs": true, "nfsd": true,
}

// ParseMounts returns the real filesystems in /proc/self/mounts, in order.
// A point mounted twice is reported once, where it was last mounted, since
// that is the filesystem statfs then sees.
func ParseMounts(b []byte) []Mount {
	var out []Mount
	for line := range strings.Lines(string(b)) {
		f := strings.Fields(line)
		if len(f) < 3 || pseudoFS[f[2]] {
			continue
		}
		m := Mount{Point: unescapeMount(f[1]), FSType: f[2]}
		out = slices.DeleteFunc(out, func(o Mount) bool { return o.Point == m.Point })
		out = append(out, m)
	}
	return out
}

// unescapeMount decodes the kernel's octal escapes in a mount point (\040
// for a space, \011, \012, \134). Anything else stays as written.
func unescapeMount(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) && isOctal(s[i+1]) && isOctal(s[i+2]) && isOctal(s[i+3]) {
			b.WriteByte((s[i+1]-'0')<<6 | (s[i+2]-'0')<<3 | (s[i+3] - '0'))
			i += 3
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func isOctal(c byte) bool { return c >= '0' && c <= '7' }

// statfs is unix.Statfs; tests replace it.
var statfs = unix.Statfs

// diskUsage is each mount's use as df computes it: total and free blocks
// in fragment-size units, available as what an unprivileged user may still
// write. A mount statfs refuses is logged and left out.
func diskUsage(mounts []Mount, stat func(string, *unix.Statfs_t) error, logf func(string, ...any)) []contract.DiskUsage {
	out := []contract.DiskUsage{}
	for _, m := range mounts {
		var st unix.Statfs_t
		if err := stat(m.Point, &st); err != nil {
			logf("system.metrics: statfs %s: %v", m.Point, err)
			continue
		}
		unit := uint64(st.Frsize)
		if unit == 0 {
			unit = uint64(st.Bsize)
		}
		out = append(out, contract.DiskUsage{
			Mount:          m.Point,
			FSType:         m.FSType,
			TotalBytes:     st.Blocks * unit,
			UsedBytes:      (st.Blocks - min(st.Bfree, st.Blocks)) * unit,
			AvailableBytes: st.Bavail * unit,
			InodesTotal:    st.Files,
			InodesFree:     st.Ffree,
		})
	}
	return out
}
