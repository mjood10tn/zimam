package metrics

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"zimam/agent/contract"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// The cpu line's first eight fields, in ticks; the per-CPU lines and
// guest/guest_nice (already inside user and nice) are left out.
func TestParseProcStat(t *testing.T) {
	got, err := ParseProcStat(fixture(t, "stat"))
	if err != nil {
		t.Fatal(err)
	}
	want := contract.CPUCounters{User: 1254871, Nice: 3021, System: 402987, Idle: 98412377, IOWait: 51234, IRQ: 0, SoftIRQ: 18765, Steal: 2210}
	if got != want {
		t.Errorf("ParseProcStat = %+v, want %+v", got, want)
	}
	for name, in := range map[string]string{
		"no cpu line":    "cpu0 1 2 3 4 5 6 7 8\nctxt 1\n",
		"short cpu line": "cpu  1 2 3\n",
		"not a number":   "cpu  1 2 x 4 5 6 7 8 0 0\n",
		"negative":       "cpu  1 2 -3 4 5 6 7 8 0 0\n",
		"empty":          "",
	} {
		if _, err := ParseProcStat([]byte(in)); err == nil {
			t.Errorf("%s: ParseProcStat(%q) accepted it", name, in)
		}
	}
	// Older kernels print fewer columns; the eight we read are there since 2.6.11.
	if got, err := ParseProcStat([]byte("cpu  1 2 3 4 5 6 7 8\n")); err != nil || got.Steal != 8 {
		t.Errorf("eight columns: %+v, %v", got, err)
	}
}

// kB values become bytes; MemAvailable, not MemFree, is "available".
func TestParseMeminfo(t *testing.T) {
	got, err := ParseMeminfo(fixture(t, "meminfo"))
	if err != nil {
		t.Fatal(err)
	}
	want := contract.MemoryUsage{TotalBytes: 3995420 * 1024, AvailableBytes: 2803112 * 1024, SwapTotalBytes: 2097148 * 1024, SwapFreeBytes: 2045948 * 1024}
	if got != want {
		t.Errorf("ParseMeminfo = %+v, want %+v", got, want)
	}
	// A server without swap reports zeros, which is an answer, not an error.
	noSwap := "MemTotal: 1000 kB\nMemAvailable: 500 kB\nSwapTotal: 0 kB\nSwapFree: 0 kB\n"
	if got, err := ParseMeminfo([]byte(noSwap)); err != nil || got.SwapTotalBytes != 0 || got.TotalBytes != 1024000 {
		t.Errorf("no swap: %+v, %v", got, err)
	}
	for name, in := range map[string]string{
		"no MemAvailable": "MemTotal: 1000 kB\nSwapTotal: 0 kB\nSwapFree: 0 kB\n",
		"no SwapFree":     "MemTotal: 1000 kB\nMemAvailable: 500 kB\nSwapTotal: 0 kB\n",
		"not a number":    "MemTotal: lots kB\nMemAvailable: 500 kB\nSwapTotal: 0 kB\nSwapFree: 0 kB\n",
		"other unit":      "MemTotal: 1000 MB\nMemAvailable: 500 kB\nSwapTotal: 0 kB\nSwapFree: 0 kB\n",
	} {
		if _, err := ParseMeminfo([]byte(in)); err == nil {
			t.Errorf("%s: ParseMeminfo accepted it", name)
		}
	}
}

func TestParseLoadavg(t *testing.T) {
	got, err := ParseLoadavg(fixture(t, "loadavg"))
	if err != nil {
		t.Fatal(err)
	}
	if want := (contract.LoadAverages{Load1: 0.42, Load5: 0.31, Load15: 0.27}); got != want {
		t.Errorf("ParseLoadavg = %+v, want %+v", got, want)
	}
	for _, in := range []string{"", "0.42 0.31\n", "a b c 1/2 3\n", "NaN 0 0 1/2 3\n", "-1 0 0 1/2 3\n", "Inf 0 0 1/2 3\n"} {
		if _, err := ParseLoadavg([]byte(in)); err == nil {
			t.Errorf("ParseLoadavg(%q) accepted it", in)
		}
	}
}

func TestParseUptime(t *testing.T) {
	got, err := ParseUptime(fixture(t, "uptime"))
	if err != nil || got != 1021453.67 {
		t.Errorf("ParseUptime = %v, %v; want 1021453.67", got, err)
	}
	for _, in := range []string{"", "x 1\n", "-5 1\n", "NaN 1\n"} {
		if _, err := ParseUptime([]byte(in)); err == nil {
			t.Errorf("ParseUptime(%q) accepted it", in)
		}
	}
}

// Bytes are summed over every interface but lo.
func TestParseNetDevSkipsLoopback(t *testing.T) {
	got, err := ParseNetDev(fixture(t, "net-dev"))
	if err != nil {
		t.Fatal(err)
	}
	want := contract.NetworkCounters{RxBytes: 52310984411 + 102400, TxBytes: 9814503921 + 51200}
	if got != want {
		t.Errorf("ParseNetDev = %+v, want %+v (lo left out)", got, want)
	}
	onlyLo := strings.Join(strings.Split(string(fixture(t, "net-dev")), "\n")[:3], "\n") + "\n"
	if got, err := ParseNetDev([]byte(onlyLo)); err != nil || got != (contract.NetworkCounters{}) {
		t.Errorf("only lo: %+v, %v; want zeros", got, err)
	}
	// Interface names without a space after the colon, as the kernel prints
	// when the counter fills the column.
	tight := "h1\nh2\n  eth0:123 1 0 0 0 0 0 0 456 1 0 0 0 0 0 0\n"
	if got, err := ParseNetDev([]byte(tight)); err != nil || got.RxBytes != 123 || got.TxBytes != 456 {
		t.Errorf("tight columns: %+v, %v", got, err)
	}
	for name, in := range map[string]string{
		"short line":   "h1\nh2\n eth0: 1 2 3\n",
		"not a number": "h1\nh2\n eth0: x 1 0 0 0 0 0 0 456 1 0 0 0 0 0 0\n",
		"no colon":     "h1\nh2\n eth0 1 1 0 0 0 0 0 0 456 1 0 0 0 0 0 0\n",
	} {
		if _, err := ParseNetDev([]byte(in)); err == nil {
			t.Errorf("%s: ParseNetDev accepted it", name)
		}
	}
}
