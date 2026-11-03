<?php

namespace Tests\Feature\Monitoring;

use App\Filament\Widgets\MetricChart;
use Illuminate\Foundation\Testing\RefreshDatabase;
use Illuminate\Support\Carbon;
use Illuminate\Support\Facades\DB;
use Tests\Support\MetricSamples;
use Tests\TestCase;

/** The charts read 1 hour, 24 hours or 7 days of samples; the week in 10-minute averages (§7.5). */
final class MetricChartTest extends TestCase
{
    use MetricSamples;
    use RefreshDatabase;

    public function testWeekQueryUses10MinuteBuckets(): void
    {
        $this->travelTo(Carbon::parse('2026-11-01 04:00:00', 'UTC'));
        $start = Carbon::parse('2026-11-01 03:00:00', 'UTC');
        for ($minute = 0; $minute < 30; $minute++) {
            $this->sample($start->copy()->addMinutes($minute), cpu: ['user' => 100 + 60 * $minute, 'idle' => 800 + 60 * $minute], rx: 1000 + 600 * $minute, load1: (float) $minute);
        }
        $this->sample(Carbon::parse('2026-10-25 03:59:00', 'UTC'), load1: 99.0); // over 7 days old: left out

        DB::enableQueryLog();
        $rows = MetricChart::rows('week');
        $sql = implode("\n", array_column(DB::getQueryLog(), 'query'));

        $this->assertStringContainsString("CAST(strftime('%s', sampled_at) / 600 AS INTEGER)", $sql);
        $this->assertCount(3, $rows);
        $this->assertEqualsWithDelta(4.5, $rows[0]['load1'], 1e-9);
        $this->assertEqualsWithDelta(14.5, $rows[1]['load1'], 1e-9);
        $this->assertEqualsWithDelta(24.5, $rows[2]['load1'], 1e-9);
        // Each bucket is dated by its samples' mean time, so rates between buckets stay right.
        $this->assertSame($start->copy()->addMinutes(4)->addSeconds(30)->timestamp, (int) $rows[0]['at']);
        $this->assertEqualsWithDelta(100 + 60 * 4.5, $rows[0]['cpu']['user'], 1e-9);

        // Ten minutes between buckets, 6000 bytes and 600 busy ticks of 1200: 10 B/s and 50%.
        $points = MetricChart::points('network', 'week');
        $this->assertCount(2, $points['labels']);
        $this->assertEqualsWithDelta(10.0, $points['datasets'][0]['data'][0], 1e-9);
        $cpu = MetricChart::points('cpu', 'week');
        $this->assertEqualsWithDelta(50.0, $cpu['datasets'][0]['data'][0], 1e-9);
    }

    /** The hour and the day read every sample, unaveraged. */
    public function testHourAndDayReadEverySample(): void
    {
        $this->travelTo(Carbon::parse('2026-11-01 04:00:00', 'UTC'));
        foreach ([90, 59, 30, 1] as $minutesAgo) {
            $this->sample(now()->subMinutes($minutesAgo), load1: (float) $minutesAgo);
        }
        $this->sample(now()->subHours(25), load1: 1500.0);

        $this->assertSame([59.0, 30.0, 1.0], array_column(MetricChart::rows('hour'), 'load1'));
        $this->assertSame([90.0, 59.0, 30.0, 1.0], array_column(MetricChart::rows('day'), 'load1'));
    }

    /** Disk use is charted per mount, as a percentage; the week averages it per bucket. */
    public function testDiskPercentPerMount(): void
    {
        $this->travelTo(Carbon::parse('2026-11-01 04:00:00', 'UTC'));
        $disk = fn (int $used): array => [
            ['mount' => '/', 'fs_type' => 'ext4', 'total_bytes' => 1000, 'used_bytes' => $used, 'available_bytes' => 1000 - $used, 'inodes_total' => 10, 'inodes_free' => 5],
            ['mount' => '/srv', 'fs_type' => 'xfs', 'total_bytes' => 100, 'used_bytes' => 10, 'available_bytes' => 90, 'inodes_total' => 10, 'inodes_free' => 5],
        ];
        $this->sample(Carbon::parse('2026-11-01 03:00:00', 'UTC'), disks: $disk(400));
        $this->sample(Carbon::parse('2026-11-01 03:01:00', 'UTC'), disks: $disk(600));

        $day = MetricChart::points('disk', 'day');
        $this->assertSame(['/', '/srv'], array_column($day['datasets'], 'label'));
        $this->assertEqualsWithDelta([40.0, 60.0], $day['datasets'][0]['data'], 1e-9);

        $week = MetricChart::points('disk', 'week');
        $this->assertEqualsWithDelta([50.0], $week['datasets'][0]['data'], 1e-9);
        $this->assertEqualsWithDelta([10.0], $week['datasets'][1]['data'], 1e-9);
    }
}
