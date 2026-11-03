<?php

namespace App\Filament\Widgets;

use App\Models\MetricSample;
use App\Support\MetricRates;
use App\Support\MetricWarnings;
use Carbon\CarbonImmutable;
use Filament\Widgets\ChartWidget;
use Illuminate\Support\Facades\DB;

/**
 * One Monitoring chart (§7.3, §7.5) over the last hour, 24 hours or 7
 * days of samples. The hour and the day plot every sample; the week
 * averages them into 10-minute buckets in SQL, about 1,000 points rather
 * than 10,000. CPU and network rates come from consecutive points, so the
 * interval across a reboot is a gap.
 */
class MetricChart extends ChartWidget
{
    public const METRICS = ['cpu', 'load', 'memory', 'network', 'disk'];

    /** Each range's length in seconds. */
    public const RANGES = ['hour' => 3600, 'day' => 86400, 'week' => 604800];

    /** The week's bucket, in seconds. */
    private const BUCKET = 600;

    protected static bool $isDiscovered = false;

    public string $metric = 'cpu';

    public ?string $filter = 'hour';

    protected ?string $pollingInterval = '60s';

    protected ?string $maxHeight = '240px';

    public function getHeading(): ?string
    {
        return __("zimam.monitoring.charts.{$this->metric}");
    }

    protected function getFilters(): ?array
    {
        return array_map(fn (string $range): string => __("zimam.monitoring.charts.ranges.{$range}"), array_combine(array_keys(self::RANGES), array_keys(self::RANGES)));
    }

    protected function getType(): string
    {
        return 'line';
    }

    protected function getData(): array
    {
        return self::points($this->metric, array_key_exists((string) $this->filter, self::RANGES) ? $this->filter : 'hour');
    }

    protected function getOptions(): array
    {
        return [
            'animation' => false,
            'elements' => ['point' => ['radius' => 0]],
            'scales' => ['y' => ['beginAtZero' => true]],
            'plugins' => ['legend' => ['display' => $this->metric !== 'cpu']],
        ];
    }

    /**
     * The samples in a range, oldest first, counters and gauges as stored;
     * for the week, their 10-minute averages, each dated by its samples'
     * mean time.
     *
     * @return list<array<string, mixed>>
     */
    public static function rows(string $range): array
    {
        $since = now()->subSeconds(self::RANGES[$range]);
        if ($range !== 'week') {
            return MetricSample::query()
                ->where('sampled_at', '>=', $since)
                ->orderBy('sampled_at')
                ->get()
                ->map(fn (MetricSample $s): array => [
                    'at' => $s->sampled_at->getTimestamp(),
                    'cpu' => $s->cpu,
                    'load1' => $s->load1,
                    'load5' => $s->load5,
                    'load15' => $s->load15,
                    'mem_total' => $s->mem_total,
                    'mem_available' => $s->mem_available,
                    'swap_total' => $s->swap_total,
                    'swap_free' => $s->swap_free,
                    'net_rx' => $s->net_rx,
                    'net_tx' => $s->net_tx,
                    'disks' => $s->disks,
                ])
                ->all();
        }
        $cpu = implode(', ', array_map(fn (string $f): string => "AVG(json_extract(cpu, '$.{$f}')) AS cpu_{$f}", MetricRates::CPU_FIELDS));

        return DB::table('metric_samples')
            ->selectRaw("CAST(strftime('%s', sampled_at) / ".self::BUCKET." AS INTEGER) AS bucket, AVG(CAST(strftime('%s', sampled_at) AS INTEGER)) AS at, {$cpu}, AVG(load1) AS load1, AVG(load5) AS load5, AVG(load15) AS load15, AVG(mem_total) AS mem_total, AVG(mem_available) AS mem_available, AVG(swap_total) AS swap_total, AVG(swap_free) AS swap_free, AVG(net_rx) AS net_rx, AVG(net_tx) AS net_tx")
            ->where('sampled_at', '>=', $since)
            ->groupBy('bucket')
            ->orderBy('bucket')
            ->get()
            ->map(function (object $row): array {
                $row = (array) $row;
                $cpu = [];
                foreach (MetricRates::CPU_FIELDS as $field) {
                    $cpu[$field] = (float) $row["cpu_{$field}"];
                }

                return [
                    'at' => (float) $row['at'],
                    'cpu' => $cpu,
                    'load1' => (float) $row['load1'],
                    'load5' => (float) $row['load5'],
                    'load15' => (float) $row['load15'],
                    'mem_total' => (float) $row['mem_total'],
                    'mem_available' => (float) $row['mem_available'],
                    'swap_total' => (float) $row['swap_total'],
                    'swap_free' => (float) $row['swap_free'],
                    'net_rx' => (float) $row['net_rx'],
                    'net_tx' => (float) $row['net_tx'],
                ];
            })
            ->all();
    }

    /**
     * Chart.js data for one metric over one range: labels and datasets,
     * with null where a value is unknown.
     *
     * @return array{labels: list<string>, datasets: list<array{label: string, data: list<?float>}>}
     */
    public static function points(string $metric, string $range): array
    {
        if ($metric === 'disk') {
            return self::diskPoints($range);
        }
        $rows = self::rows($range);
        $series = __('zimam.monitoring.charts.series');
        if ($metric === 'cpu' || $metric === 'network') {
            $rates = MetricRates::series($rows);
            $labels = array_map(fn (array $r): string => self::label($r['at'], $range), $rates);

            return $metric === 'cpu'
                ? ['labels' => $labels, 'datasets' => [['label' => $series['cpu'], 'data' => array_column($rates, 'cpu')]]]
                : ['labels' => $labels, 'datasets' => [
                    ['label' => $series['rx'], 'data' => array_column($rates, 'rx')],
                    ['label' => $series['tx'], 'data' => array_column($rates, 'tx')],
                ]];
        }
        $labels = array_map(fn (array $r): string => self::label($r['at'], $range), $rows);
        if ($metric === 'load') {
            return ['labels' => $labels, 'datasets' => array_map(fn (string $f): array => ['label' => $series[$f], 'data' => array_map(fn (array $r): float => (float) $r[$f], $rows)], ['load1', 'load5', 'load15'])];
        }

        return ['labels' => $labels, 'datasets' => [
            ['label' => $series['memory'], 'data' => array_map(fn (array $r): ?float => $r['mem_total'] > 0 ? 100.0 * (1 - $r['mem_available'] / $r['mem_total']) : null, $rows)],
            ['label' => $series['swap'], 'data' => array_map(fn (array $r): ?float => $r['swap_total'] > 0 ? 100.0 * (1 - $r['swap_free'] / $r['swap_total']) : null, $rows)],
        ]];
    }

    /**
     * Each mount's used percentage; the week averages it per bucket with
     * json_each over the stored disks.
     *
     * @return array{labels: list<string>, datasets: list<array{label: string, data: list<?float>}>}
     */
    private static function diskPoints(string $range): array
    {
        /** @var array<int|string, array<string, ?float>> $byTime time => mount => percent */
        $byTime = [];
        if ($range !== 'week') {
            foreach (self::rows($range) as $row) {
                foreach ($row['disks'] as $disk) {
                    $byTime[$row['at']][$disk['mount']] = MetricWarnings::diskPercent($disk);
                }
                $byTime[$row['at']] ??= [];
            }
        } else {
            $rows = DB::query()
                ->fromRaw('metric_samples, json_each(metric_samples.disks) AS d')
                ->selectRaw("CAST(strftime('%s', sampled_at) / ".self::BUCKET." AS INTEGER) AS bucket, MIN(CAST(strftime('%s', sampled_at) AS INTEGER)) AS at, json_extract(d.value, '$.mount') AS mount, AVG(100.0 * json_extract(d.value, '$.used_bytes') / (json_extract(d.value, '$.used_bytes') + json_extract(d.value, '$.available_bytes'))) AS percent")
                ->where('sampled_at', '>=', now()->subSeconds(self::RANGES['week']))
                ->groupBy('bucket', 'mount')
                ->orderBy('bucket')
                ->orderBy('mount')
                ->get();
            $times = [];
            foreach ($rows as $row) {
                $times[$row->bucket] ??= (int) $row->at;
                $byTime[$times[$row->bucket]][$row->mount] = $row->percent === null ? null : (float) $row->percent;
            }
        }
        $mounts = [];
        foreach ($byTime as $disks) {
            foreach (array_keys($disks) as $mount) {
                $mounts[$mount] = true;
            }
        }

        return [
            'labels' => array_map(fn (int|float $at): string => self::label($at, $range), array_keys($byTime)),
            'datasets' => array_map(fn (string $mount): array => [
                'label' => $mount,
                'data' => array_values(array_map(fn (array $disks): ?float => $disks[$mount] ?? null, $byTime)),
            ], array_map('strval', array_keys($mounts))),
        ];
    }

    private static function label(int|float $at, string $range): string
    {
        return Warnings::time(CarbonImmutable::createFromTimestamp((int) round($at)), $range === 'week' ? 'j M H:i' : 'H:i');
    }
}
