<?php

namespace App\Models;

use Carbon\CarbonImmutable;
use Illuminate\Database\Eloquent\Model;

/**
 * One minute's system.metrics (§7.5, §7.6): CPU and network counters as the
 * agent reported them, load, memory and swap, each mount's use and each
 * monitored service's state. Written once, pruned after seven days.
 *
 * @property CarbonImmutable $sampled_at
 * @property array<string, int> $cpu
 * @property list<array<string, mixed>> $disks
 * @property list<array{unit: string, active_state: string, unit_file_state: string}> $services
 */
class MetricSample extends Model
{
    /** Samples are kept this long (§7.5). */
    public const KEEP_DAYS = 7;

    public $timestamps = false;

    protected $fillable = [
        'sampled_at', 'cpu', 'load1', 'load5', 'load15', 'mem_total', 'mem_available',
        'swap_total', 'swap_free', 'disks', 'net_rx', 'net_tx', 'services',
    ];

    protected function casts(): array
    {
        return [
            'sampled_at' => 'immutable_datetime',
            'cpu' => 'array',
            'load1' => 'float',
            'load5' => 'float',
            'load15' => 'float',
            'mem_total' => 'integer',
            'mem_available' => 'integer',
            'swap_total' => 'integer',
            'swap_free' => 'integer',
            'disks' => 'array',
            'net_rx' => 'integer',
            'net_tx' => 'integer',
            'services' => 'array',
        ];
    }

    /**
     * The row for one system.metrics answer.
     *
     * @param  array<string, mixed>  $data  system.metrics's data
     * @return array<string, mixed>
     */
    public static function attributesFrom(array $data): array
    {
        return [
            'cpu' => $data['cpu'],
            'load1' => $data['load']['load1'],
            'load5' => $data['load']['load5'],
            'load15' => $data['load']['load15'],
            'mem_total' => $data['memory']['total_bytes'],
            'mem_available' => $data['memory']['available_bytes'],
            'swap_total' => $data['memory']['swap_total_bytes'],
            'swap_free' => $data['memory']['swap_free_bytes'],
            'disks' => $data['disks'],
            'net_rx' => $data['network']['rx_bytes'],
            'net_tx' => $data['network']['tx_bytes'],
            'services' => $data['services'],
        ];
    }
}
