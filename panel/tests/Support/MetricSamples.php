<?php

namespace Tests\Support;

use App\Models\MetricSample;
use Carbon\CarbonInterface;

/** Builds metric_samples rows for tests: counters as the agent reports them. */
trait MetricSamples
{
    /**
     * @param  array<string, int>  $cpu  counters to put over the defaults
     * @param  list<array<string, mixed>>|null  $disks
     */
    protected function sample(CarbonInterface $at, array $cpu = [], int $rx = 1000, int $tx = 500, float $load1 = 0.5, int $memAvailable = 2048, ?array $disks = null): MetricSample
    {
        return MetricSample::query()->create([
            'sampled_at' => $at,
            'cpu' => array_replace(['user' => 100, 'nice' => 0, 'system' => 50, 'idle' => 800, 'iowait' => 50, 'irq' => 0, 'softirq' => 0, 'steal' => 0], $cpu),
            'load1' => $load1,
            'load5' => 0.4,
            'load15' => 0.3,
            'mem_total' => 4096,
            'mem_available' => $memAvailable,
            'swap_total' => 0,
            'swap_free' => 0,
            'disks' => $disks ?? [['mount' => '/', 'fs_type' => 'ext4', 'total_bytes' => 1000, 'used_bytes' => 400, 'available_bytes' => 600, 'inodes_total' => 100, 'inodes_free' => 90]],
            'net_rx' => $rx,
            'net_tx' => $tx,
            'services' => [],
        ]);
    }
}
