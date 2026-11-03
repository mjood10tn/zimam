<?php

namespace Tests\Feature\Monitoring;

use App\Agent\AgentClient;
use App\Agent\Commands\SystemMetrics;
use App\Agent\Exceptions\AgentUnavailable;
use App\Agent\FakeAgentClient;
use App\Models\MetricSample;
use Illuminate\Console\Scheduling\Event;
use Illuminate\Console\Scheduling\Schedule;
use Illuminate\Foundation\Testing\RefreshDatabase;
use Illuminate\Support\Carbon;
use Illuminate\Support\Facades\Artisan;
use Tests\TestCase;

/** zimam:sample-metrics stores one system.metrics answer a minute (§7.5). */
final class SampleMetricsTest extends TestCase
{
    use RefreshDatabase;

    public function testSampleStored(): void
    {
        $this->travelTo(Carbon::parse('2026-11-01 03:41:00', 'UTC'));
        $agent = new FakeAgentClient;
        $this->app->instance(AgentClient::class, $agent);
        $data = $agent->call(new SystemMetrics)->data;
        $agent->respond('system.metrics', $data);

        $this->assertSame(0, Artisan::call('zimam:sample-metrics'));

        $this->assertSame(1, MetricSample::query()->count());
        $sample = MetricSample::query()->sole();
        $this->assertTrue($sample->sampled_at->eq(Carbon::parse('2026-11-01 03:41:00', 'UTC')));
        $this->assertSame($data['cpu'], $sample->cpu);
        $this->assertSame($data['load']['load1'], $sample->load1);
        $this->assertSame($data['load']['load5'], $sample->load5);
        $this->assertSame($data['load']['load15'], $sample->load15);
        $this->assertSame($data['memory']['total_bytes'], $sample->mem_total);
        $this->assertSame($data['memory']['available_bytes'], $sample->mem_available);
        $this->assertSame($data['memory']['swap_total_bytes'], $sample->swap_total);
        $this->assertSame($data['memory']['swap_free_bytes'], $sample->swap_free);
        $this->assertSame($data['disks'], $sample->disks);
        $this->assertSame($data['network']['rx_bytes'], $sample->net_rx);
        $this->assertSame($data['network']['tx_bytes'], $sample->net_tx);
        $this->assertSame($data['services'], $sample->services);
        $this->assertContains('system.metrics', array_column($agent->calls(), 'command'));
    }

    /** No answer, no row: a gap in the chart, never a sample of zeros. */
    public function testNothingStoredWhenAgentDown(): void
    {
        $agent = (new FakeAgentClient)->respond('system.metrics', new AgentUnavailable('connect: no such file'));
        $this->app->instance(AgentClient::class, $agent);

        $this->assertSame(1, Artisan::call('zimam:sample-metrics'));
        $this->assertSame(0, MetricSample::query()->count());
    }

    public function testRunsEveryMinute(): void
    {
        $events = collect(app(Schedule::class)->events())
            ->filter(fn (Event $event): bool => str_contains((string) $event->command, 'zimam:sample-metrics'));

        $this->assertCount(1, $events);
        $this->assertSame('* * * * *', $events->first()->expression);
    }
}
