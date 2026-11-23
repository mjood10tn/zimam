<?php

namespace App\Support;

use App\Filament\Widgets\Warnings;
use App\Models\MetricSample;
use Carbon\CarbonImmutable;

/**
 * The warnings system.metrics raises (§7.5), on the Monitoring page and
 * the dashboard alike: a disk over 85% full, under 10% of memory
 * available, and any monitored service that isn't active. A failed service
 * is critical; one merely stopped or starting is a warning. A site's worker
 * in a crash loop warns, and one systemd gave up on is critical (R04-D8);
 * those carry the key worker-crashloop:<site>:<worker id> (R04-D17). Every
 * warning has a key that stays the same while it lasts (disk:<mount>,
 * memory, service:<unit>), which the notifications debounce on (R05-D12).
 */
final class MetricWarnings
{
    public const DISK_PERCENT = 85;

    public const MEMORY_AVAILABLE_PERCENT = 10;

    /** A failing worker warns once its restarts rose by more than this within WORKER_WINDOW_MINUTES. */
    public const WORKER_RESTARTS = 5;

    public const WORKER_WINDOW_MINUTES = 10;

    /**
     * @param  array<string, mixed>  $metrics  system.metrics's data
     * @return list<array{severity: 'critical'|'warning', title: string, detail: string, at: string, key: string}>
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
                    'key' => "disk:{$disk['mount']}",
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
                'key' => 'memory',
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
                'key' => "service:{$service['unit']}",
            ];
        }
        $history = null;
        foreach ($metrics['workers'] ?? [] as $worker) {
            // NRestarts counts clean exits too (queue:restart, --max-time), so
            // only a worker whose last exit failed can be crash-looping.
            if (! $worker['failed'] && ! $worker['failing']) {
                continue;
            }
            $history ??= self::workerHistory($at);
            if (! $worker['failed'] && self::restartsRose($history, $worker) <= self::WORKER_RESTARTS) {
                continue;
            }
            $lang = $worker['failed'] ? 'worker_failed' : 'worker_crashloop';
            $warnings[] = [
                'severity' => $worker['failed'] ? 'critical' : 'warning',
                'title' => __("zimam.monitoring.warnings.{$lang}.title", ['site' => $worker['site'], 'worker' => $worker['worker']]),
                'detail' => __("zimam.monitoring.warnings.{$lang}.detail"),
                'at' => $checked,
                'key' => "worker-crashloop:{$worker['site']}:{$worker['worker']}",
            ];
        }

        return $warnings;
    }

    /**
     * The workers of the samples in the window before $at, oldest first.
     *
     * @return list<list<array<string, mixed>>>
     */
    private static function workerHistory(CarbonImmutable $at): array
    {
        return MetricSample::query()
            ->where('sampled_at', '>=', $at->subMinutes(self::WORKER_WINDOW_MINUTES))
            ->where('sampled_at', '<', $at)
            ->orderBy('sampled_at')
            ->pluck('workers')
            ->filter(fn (mixed $workers): bool => is_array($workers))
            ->values()
            ->all();
    }

    /**
     * How far a worker's restarts rose over the samples and now. A drop is
     * a reset (reset-failed sets NRestarts to 0), so it counts from 0.
     *
     * @param  list<list<array<string, mixed>>>  $history
     * @param  array<string, mixed>  $worker
     */
    private static function restartsRose(array $history, array $worker): int
    {
        $counts = [];
        foreach ($history as $workers) {
            foreach ($workers as $w) {
                if ($w['site'] === $worker['site'] && $w['worker'] === $worker['worker']) {
                    $counts[] = (int) $w['restarts'];
                }
            }
        }
        $counts[] = (int) $worker['restarts'];
        $rose = 0;
        for ($i = 1; $i < count($counts); $i++) {
            $rose += $counts[$i] >= $counts[$i - 1] ? $counts[$i] - $counts[$i - 1] : $counts[$i];
        }

        return $rose;
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
