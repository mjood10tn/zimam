// Package metrics answers system.metrics (design §7.5): uptime, load, CPU
// and network counters and memory from /proc, disk use from statfs, and the
// monitored services from systemctl. It only reads.
package metrics

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"

	"zimam/agent/contract"
	"zimam/agent/internal/sysexec"
)

// Collect gathers everything system.metrics returns. A /proc file that
// can't be read or parsed, or a systemctl that fails, fails the call: the
// panel would chart a zero as a real value. A mount statfs can't read is
// left out, so one bad mount doesn't blank the others.
func Collect(ctx context.Context, d sysexec.Deps) (contract.SystemMetricsResult, error) {
	var m contract.SystemMetricsResult
	var err error
	read := func(path string, parse func([]byte) error) {
		if err != nil {
			return
		}
		var b []byte
		if b, err = os.ReadFile(path); err != nil {
			return
		}
		if perr := parse(b); perr != nil {
			err = fmt.Errorf("%s: %w", path, perr)
		}
	}
	read(d.Paths.ProcUptime(), func(b []byte) (e error) { m.UptimeSeconds, e = ParseUptime(b); return })
	read(d.Paths.ProcLoadavg(), func(b []byte) (e error) { m.Load, e = ParseLoadavg(b); return })
	read(d.Paths.ProcStat(), func(b []byte) (e error) { m.CPU, e = ParseProcStat(b); return })
	read(d.Paths.ProcMeminfo(), func(b []byte) (e error) { m.Memory, e = ParseMeminfo(b); return })
	read(d.Paths.ProcNetDev(), func(b []byte) (e error) { m.Network, e = ParseNetDev(b); return })
	read(d.Paths.ProcMounts(), func(b []byte) error {
		m.Disks = diskUsage(ParseMounts(b), statfs, d.Logf)
		return nil
	})
	if err != nil {
		return contract.SystemMetricsResult{}, err
	}
	if m.Services, err = Services(ctx, d); err != nil {
		return contract.SystemMetricsResult{}, err
	}
	return m, nil
}

// ParseUptime reads /proc/uptime's first field, the seconds since boot.
func ParseUptime(b []byte) (float64, error) {
	f := strings.Fields(string(b))
	if len(f) < 1 {
		return 0, errors.New("empty")
	}
	return parseGauge(f[0])
}

// ParseLoadavg reads /proc/loadavg's three load averages.
func ParseLoadavg(b []byte) (contract.LoadAverages, error) {
	f := strings.Fields(string(b))
	if len(f) < 3 {
		return contract.LoadAverages{}, fmt.Errorf("%d fields, want at least 3", len(f))
	}
	var v [3]float64
	for i := range v {
		var err error
		if v[i], err = parseGauge(f[i]); err != nil {
			return contract.LoadAverages{}, err
		}
	}
	return contract.LoadAverages{Load1: v[0], Load5: v[1], Load15: v[2]}, nil
}

// parseGauge accepts a finite, non-negative decimal.
func parseGauge(s string) (float64, error) {
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, err
	}
	if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 {
		return 0, fmt.Errorf("%q isn't a finite, non-negative number", s)
	}
	return v, nil
}

// ParseProcStat reads the first eight counters of /proc/stat's "cpu" line,
// the sum over every CPU: user nice system idle iowait irq softirq steal.
func ParseProcStat(b []byte) (contract.CPUCounters, error) {
	for line := range strings.Lines(string(b)) {
		f := strings.Fields(line)
		if len(f) == 0 || f[0] != "cpu" {
			continue
		}
		if len(f) < 9 {
			return contract.CPUCounters{}, fmt.Errorf("the cpu line has %d counters, want at least 8", len(f)-1)
		}
		var v [8]uint64
		for i := range v {
			var err error
			if v[i], err = strconv.ParseUint(f[i+1], 10, 64); err != nil {
				return contract.CPUCounters{}, err
			}
		}
		return contract.CPUCounters{User: v[0], Nice: v[1], System: v[2], Idle: v[3], IOWait: v[4], IRQ: v[5], SoftIRQ: v[6], Steal: v[7]}, nil
	}
	return contract.CPUCounters{}, errors.New("no cpu line")
}

// ParseMeminfo reads MemTotal, MemAvailable, SwapTotal and SwapFree, which
// the kernel prints in kB (KiB), as bytes.
func ParseMeminfo(b []byte) (contract.MemoryUsage, error) {
	want := map[string]*uint64{}
	var m contract.MemoryUsage
	want["MemTotal"], want["MemAvailable"], want["SwapTotal"], want["SwapFree"] = &m.TotalBytes, &m.AvailableBytes, &m.SwapTotalBytes, &m.SwapFreeBytes
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		key, rest, ok := strings.Cut(sc.Text(), ":")
		dst, wanted := want[key]
		if !ok || !wanted {
			continue
		}
		f := strings.Fields(rest)
		if len(f) != 2 || f[1] != "kB" {
			return contract.MemoryUsage{}, fmt.Errorf("%s: %q isn't a size in kB", key, strings.TrimSpace(rest))
		}
		kb, err := strconv.ParseUint(f[0], 10, 64)
		if err != nil {
			return contract.MemoryUsage{}, fmt.Errorf("%s: %w", key, err)
		}
		if kb > math.MaxUint64/1024 {
			return contract.MemoryUsage{}, fmt.Errorf("%s: %d kB is out of range", key, kb)
		}
		*dst = kb * 1024
		delete(want, key)
	}
	if len(want) > 0 {
		var missing []string
		for k := range want {
			missing = append(missing, k)
		}
		return contract.MemoryUsage{}, fmt.Errorf("missing %s", strings.Join(missing, ", "))
	}
	return m, sc.Err()
}

// ParseNetDev sums received and sent bytes over every interface but lo.
// After the two header lines each line is "<name>: <8 receive counters> <8
// transmit counters>"; bytes are the first of each group.
func ParseNetDev(b []byte) (contract.NetworkCounters, error) {
	var n contract.NetworkCounters
	lines := strings.Split(string(b), "\n")
	for i, line := range lines {
		if i < 2 || strings.TrimSpace(line) == "" {
			continue
		}
		name, rest, ok := strings.Cut(line, ":")
		if !ok {
			return contract.NetworkCounters{}, fmt.Errorf("line %d has no interface name", i+1)
		}
		f := strings.Fields(rest)
		if len(f) < 16 {
			return contract.NetworkCounters{}, fmt.Errorf("%s: %d counters, want 16", strings.TrimSpace(name), len(f))
		}
		rx, err := strconv.ParseUint(f[0], 10, 64)
		if err != nil {
			return contract.NetworkCounters{}, err
		}
		tx, err := strconv.ParseUint(f[8], 10, 64)
		if err != nil {
			return contract.NetworkCounters{}, err
		}
		if strings.TrimSpace(name) == "lo" {
			continue
		}
		n.RxBytes += rx
		n.TxBytes += tx
	}
	return n, nil
}
