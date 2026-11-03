<?php

namespace Tests\Feature\Monitoring;

use App\Support\MetricRates;
use Tests\TestCase;

/** CPU % and network rates come from consecutive samples' counters (§7.5). */
final class MetricRatesTest extends TestCase
{
    private const BEFORE = ['user' => 100, 'nice' => 0, 'system' => 50, 'idle' => 800, 'iowait' => 50, 'irq' => 0, 'softirq' => 0, 'steal' => 0];

    public function testCpuPercentFromCounters(): void
    {
        // 200 ticks passed: user 60, system 20, idle 100, iowait 20.
        // Idle and iowait are 120 of them, so the CPUs were busy 40% of the time.
        $after = array_replace(self::BEFORE, ['user' => 160, 'system' => 70, 'idle' => 900, 'iowait' => 70]);
        $this->assertEqualsWithDelta(40.0, MetricRates::cpuPercent(self::BEFORE, $after), 1e-9);

        // steal, irq and softirq count as busy; nice too.
        $after = array_replace(self::BEFORE, ['nice' => 10, 'irq' => 10, 'softirq' => 10, 'steal' => 10, 'idle' => 860]);
        $this->assertEqualsWithDelta(40.0, MetricRates::cpuPercent(self::BEFORE, $after), 1e-9);

        // No tick passed: no answer rather than a division by zero.
        $this->assertNull(MetricRates::cpuPercent(self::BEFORE, self::BEFORE));
    }

    public function testNetworkRate(): void
    {
        $this->assertEqualsWithDelta(100.0, MetricRates::perSecond(1000, 7000, 60.0), 1e-9);
        $this->assertSame(0.0, MetricRates::perSecond(1000, 1000, 60.0));
        $this->assertNull(MetricRates::perSecond(1000, 2000, 0.0));

        $series = MetricRates::series([
            ['at' => 0, 'cpu' => self::BEFORE, 'net_rx' => 1000, 'net_tx' => 500],
            ['at' => 60, 'cpu' => array_replace(self::BEFORE, ['user' => 160, 'system' => 70, 'idle' => 900, 'iowait' => 70]), 'net_rx' => 7000, 'net_tx' => 3500],
        ]);
        $this->assertCount(1, $series);
        $this->assertSame(60, $series[0]['at']);
        $this->assertEqualsWithDelta(40.0, $series[0]['cpu'], 1e-9);
        $this->assertEqualsWithDelta(100.0, $series[0]['rx'], 1e-9);
        $this->assertEqualsWithDelta(50.0, $series[0]['tx'], 1e-9);
    }

    /** After a reboot the counters start again from zero: that interval is skipped, the next one counts. */
    public function testCounterResetSkipped(): void
    {
        $this->assertNull(MetricRates::cpuPercent(self::BEFORE, array_replace(self::BEFORE, ['user' => 5, 'idle' => 900])));
        $this->assertNull(MetricRates::perSecond(7000, 100, 60.0));

        $series = MetricRates::series([
            ['at' => 0, 'cpu' => self::BEFORE, 'net_rx' => 9000, 'net_tx' => 9000],
            ['at' => 60, 'cpu' => ['user' => 10, 'nice' => 0, 'system' => 5, 'idle' => 80, 'iowait' => 5, 'irq' => 0, 'softirq' => 0, 'steal' => 0], 'net_rx' => 100, 'net_tx' => 100],
            ['at' => 120, 'cpu' => ['user' => 30, 'nice' => 0, 'system' => 5, 'idle' => 160, 'iowait' => 5, 'irq' => 0, 'softirq' => 0, 'steal' => 0], 'net_rx' => 700, 'net_tx' => 400],
        ]);
        $this->assertCount(2, $series);
        $this->assertNull($series[0]['cpu']);
        $this->assertNull($series[0]['rx']);
        $this->assertNull($series[0]['tx']);
        $this->assertEqualsWithDelta(20.0, $series[1]['cpu'], 1e-9);
        $this->assertEqualsWithDelta(10.0, $series[1]['rx'], 1e-9);
        $this->assertEqualsWithDelta(5.0, $series[1]['tx'], 1e-9);
    }
}
