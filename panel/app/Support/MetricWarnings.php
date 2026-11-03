<?php

namespace App\Support;

use App\Filament\Widgets\Warnings;
use Carbon\CarbonImmutable;

/**
 * The warnings system.metrics raises (§7.5), on the Monitoring page and
 * the dashboard alike: a disk over 85% full, under 10% of memory
 * available, and any monitored service that isn't active. A failed service
 * is critical; one merely stopped or starting is a warning.
 */
final class MetricWarnings
{
    public const DISK_PERCENT = 85;

    public const MEMORY_AVAILABLE_PERCENT = 10;

    /**
     * @param  array<string, mixed>  $metrics  system.metrics's data
     * @return list<array{severity: 'critical'|'warning', title: string, detail: string, at: string}>
     */
    public static function from(array $metrics, CarbonImmutable $at): array
    {
        $checked = __('zimam.dashboard.checked', ['time' => Warnings::time($at, 'H:i')]);
        $warnings = [];
        foreach ($metrics['disks'] as $disk) {
            $percent = self::diskPercent($disk);
            if ($percent !== null && $percent > self::DISK_PERCENT) {
                $warnings[] = [
                    'severity' => 'warning',
                    'title' => __('zimam.monitoring.warnings.disk.title', ['mount' => $disk['mount'], 'percent' => (int) floor($percent)]),
                    'detail' => __('zimam.monitoring.warnings.disk.detail'),
                    'at' => $checked,
                ];
            }
        }
        $memory = self::memoryAvailablePercent($metrics['memory']);
        if ($memory !== null && $memory < self::MEMORY_AVAILABLE_PERCENT) {
            $warnings[] = [
                'severity' => 'warning',
                'title' => __('zimam.monitoring.warnings.memory.title', ['percent' => (int) floor($memory)]),
                'detail' => __('zimam.monitoring.warnings.memory.detail'),
                'at' => $checked,
            ];
        }
        foreach ($metrics['services'] as $service) {
            if ($service['active_state'] === 'active') {
                continue;
            }
            $failed = $service['active_state'] === 'failed';
            $warnings[] = [
                'severity' => $failed ? 'critical' : 'warning',
                'title' => __('zimam.monitoring.warnings.service.title', ['unit' => $service['unit'], 'state' => $service['active_state']]),
                'detail' => __('zimam.monitoring.warnings.service.detail', ['unit' => $service['unit']]),
                'at' => $checked,
            ];
        }

        return $warnings;
    }

    /** Used space as df shows it, used of what root and users can use: null for an empty filesystem. @param array<string, int> $disk */
    public static function diskPercent(array $disk): ?float
    {
        $usable = $disk['used_bytes'] + $disk['available_bytes'];

        return $usable > 0 ? 100.0 * $disk['used_bytes'] / $usable : null;
    }

    /** @param array<string, int> $memory */
    public static function memoryAvailablePercent(array $memory): ?float
    {
        return $memory['total_bytes'] > 0 ? 100.0 * $memory['available_bytes'] / $memory['total_bytes'] : null;
    }
}
