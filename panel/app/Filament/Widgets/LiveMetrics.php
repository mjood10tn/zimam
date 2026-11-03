<?php

namespace App\Filament\Widgets;

use App\Agent\MetricsReading;
use App\Models\MetricSample;
use App\Support\Bytes;
use App\Support\MetricRates;
use App\Support\MetricWarnings;
use Filament\Widgets\Widget;

/**
 * The Monitoring page's live values (§7.3, §7.5), from a direct
 * system.metrics call every 5 seconds, with the warnings they raise. CPU
 * and network rates need two readings: the first comes from the latest
 * stored sample, so they show at once when the scheduler is sampling.
 */
class LiveMetrics extends Widget
{
    protected static bool $isDiscovered = false;

    protected static ?bool $isLazyLoaded = false;

    protected string $view = 'filament.widgets.live-metrics';

    /** How old a stored sample may be to start the rates from. */
    private const SEED_MINUTES = 10;

    /** @var array{at: int, cpu: array<string, int>, net_rx: int, net_tx: int}|null the reading the rates were last taken from */
    public ?array $previous = null;

    /** @var array{cpu: ?float, rx: ?float, tx: ?float}|null */
    public ?array $rates = null;

    public function mount(): void
    {
        $sample = MetricSample::query()
            ->where('sampled_at', '>=', now()->subMinutes(self::SEED_MINUTES))
            ->latest('sampled_at')
            ->first();
        if ($sample !== null) {
            $this->previous = ['at' => $sample->sampled_at->getTimestamp(), 'cpu' => $sample->cpu, 'net_rx' => $sample->net_rx, 'net_tx' => $sample->net_tx];
        }
    }

    /** @return array<string, mixed> */
    protected function getViewData(): array
    {
        $reading = MetricsReading::current();
        if ($reading === null) {
            return ['reading' => null, 'rows' => [], 'disks' => [], 'warnings' => []];
        }
        $this->measure($reading);
        $data = $reading->data;

        return [
            'reading' => $reading,
            'rows' => $this->rows($data),
            'disks' => $this->disks($data['disks']),
            'warnings' => MetricWarnings::from($data, $reading->at),
        ];
    }

    /** Takes the rates since the previous reading, when this one is newer. */
    private function measure(MetricsReading $reading): void
    {
        $current = [
            'at' => $reading->at->getTimestamp(),
            'cpu' => $reading->data['cpu'],
            'net_rx' => $reading->data['network']['rx_bytes'],
            'net_tx' => $reading->data['network']['tx_bytes'],
        ];
        if ($this->previous !== null && $this->previous['at'] >= $current['at']) {
            return;
        }
        if ($this->previous !== null) {
            $rate = MetricRates::series([$this->previous, $current])[0];
            $this->rates = ['cpu' => $rate['cpu'], 'rx' => $rate['rx'], 'tx' => $rate['tx']];
        }
        $this->previous = $current;
    }

    /**
     * @param  array<string, mixed>  $data
     * @return list<array{string, string}>
     */
    private function rows(array $data): array
    {
        $measuring = __('zimam.monitoring.live.measuring');
        $cpu = $this->rates['cpu'] ?? null;
        $rx = $this->rates['rx'] ?? null;
        $tx = $this->rates['tx'] ?? null;
        $memory = $data['memory'];
        $uptime = (int) $data['uptime_seconds'];
        $days = intdiv($uptime, 86400);

        return [
            [__('zimam.monitoring.live.cpu'), $cpu === null ? $measuring : __('zimam.monitoring.live.cpu_value', ['percent' => (int) round($cpu)])],
            [__('zimam.monitoring.live.load'), __('zimam.monitoring.live.load_value', [
                'load1' => number_format($data['load']['load1'], 2),
                'load5' => number_format($data['load']['load5'], 2),
                'load15' => number_format($data['load']['load15'], 2),
            ])],
            [__('zimam.monitoring.live.memory'), __('zimam.monitoring.live.memory_value', [
                'available' => Bytes::format($memory['available_bytes']),
                'total' => Bytes::format($memory['total_bytes']),
            ])],
            [__('zimam.monitoring.live.swap'), $memory['swap_total_bytes'] === 0
                ? __('zimam.monitoring.live.swap_none')
                : __('zimam.monitoring.live.swap_value', [
                    'used' => Bytes::format($memory['swap_total_bytes'] - $memory['swap_free_bytes']),
                    'total' => Bytes::format($memory['swap_total_bytes']),
                ])],
            [__('zimam.monitoring.live.network'), $rx === null || $tx === null ? $measuring : __('zimam.monitoring.live.network_value', [
                'rx' => __('zimam.monitoring.live.per_second', ['size' => Bytes::format($rx)]),
                'tx' => __('zimam.monitoring.live.per_second', ['size' => Bytes::format($tx)]),
            ])],
            [__('zimam.monitoring.live.uptime'), trans_choice('zimam.monitoring.live.uptime_value', $days, ['count' => $days, 'hours' => intdiv($uptime % 86400, 3600)])],
        ];
    }

    /**
     * @param  list<array<string, mixed>>  $disks
     * @return list<array{mount: string, usage: string, inodes: ?string}>
     */
    private function disks(array $disks): array
    {
        return array_map(fn (array $disk): array => [
            'mount' => $disk['mount'],
            'usage' => __('zimam.monitoring.live.disk_value', [
                'percent' => (int) floor(MetricWarnings::diskPercent($disk) ?? 0),
                'available' => Bytes::format($disk['available_bytes']),
                'total' => Bytes::format($disk['total_bytes']),
            ]),
            'inodes' => $disk['inodes_total'] > 0
                ? __('zimam.monitoring.live.inodes_value', ['percent' => (int) floor(100 * ($disk['inodes_total'] - $disk['inodes_free']) / $disk['inodes_total'])])
                : null,
        ], $disks);
    }
}
