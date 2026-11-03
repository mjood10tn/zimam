package contract

func init() {
	Register(Descriptor{
		Name:   "system.metrics",
		Kind:   KindRead,
		Params: func() Params { return &SystemMetricsParams{} },
		Result: SystemMetricsResult{},
	})
}

type SystemMetricsParams struct{}

func (SystemMetricsParams) Validate() error { return nil }

// SystemMetricsResult is §7.5's system.metrics: what the panel samples every
// minute and shows live. Counters are cumulative since boot, so the panel
// works out rates from two samples. Its slices are empty, never null.
type SystemMetricsResult struct {
	UptimeSeconds float64         `json:"uptime_seconds"` // /proc/uptime
	Load          LoadAverages    `json:"load"`           // /proc/loadavg
	CPU           CPUCounters     `json:"cpu"`            // the cpu line of /proc/stat
	Memory        MemoryUsage     `json:"memory"`         // /proc/meminfo
	Disks         []DiskUsage     `json:"disks"`          // statfs of each real mount
	Network       NetworkCounters `json:"network"`        // /proc/net/dev, loopback left out
	Services      []ServiceState  `json:"services"`       // the monitored units
}

type LoadAverages struct {
	Load1  float64 `json:"load1"`
	Load5  float64 `json:"load5"`
	Load15 float64 `json:"load15"`
}

// CPUCounters are the cpu line's first eight fields, in USER_HZ ticks.
// guest and guest_nice are left out: the kernel already counts them in user
// and nice.
type CPUCounters struct {
	User    uint64 `json:"user"`
	Nice    uint64 `json:"nice"`
	System  uint64 `json:"system"`
	Idle    uint64 `json:"idle"`
	IOWait  uint64 `json:"iowait"`
	IRQ     uint64 `json:"irq"`
	SoftIRQ uint64 `json:"softirq"`
	Steal   uint64 `json:"steal"`
}

// MemoryUsage is in bytes. Available is MemAvailable, the kernel's estimate
// of what can be used without swapping.
type MemoryUsage struct {
	TotalBytes     uint64 `json:"total_bytes"`
	AvailableBytes uint64 `json:"available_bytes"`
	SwapTotalBytes uint64 `json:"swap_total_bytes"`
	SwapFreeBytes  uint64 `json:"swap_free_bytes"`
}

// DiskUsage is one mount, as df counts it: used is total less free, and
// available is what a user other than root can still write.
type DiskUsage struct {
	Mount          string `json:"mount"`
	FSType         string `json:"fs_type"`
	TotalBytes     uint64 `json:"total_bytes"`
	UsedBytes      uint64 `json:"used_bytes"`
	AvailableBytes uint64 `json:"available_bytes"`
	InodesTotal    uint64 `json:"inodes_total"` // 0 on filesystems without a fixed inode table (btrfs)
	InodesFree     uint64 `json:"inodes_free"`
}

// NetworkCounters are summed over every interface except loopback.
type NetworkCounters struct {
	RxBytes uint64 `json:"rx_bytes"`
	TxBytes uint64 `json:"tx_bytes"`
}

// ServiceState is one monitored unit as systemctl show reports it.
type ServiceState struct {
	Unit          string `json:"unit"`            // nginx.service, zimam-agent.socket, …
	ActiveState   string `json:"active_state"`    // active, inactive, failed, activating, …
	UnitFileState string `json:"unit_file_state"` // enabled, disabled, static, … ("" for a unit that isn't installed)
}
