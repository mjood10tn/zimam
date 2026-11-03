<?php

namespace Tests\Feature\Monitoring;

use App\Agent\AgentClient;
use App\Agent\Commands\SystemMetrics;
use App\Agent\FakeAgentClient;
use App\Filament\Widgets\LiveMetrics;
use App\Filament\Widgets\ServiceStates;
use App\Models\User;
use App\Support\MetricWarnings;
use Carbon\CarbonImmutable;
use Illuminate\Foundation\Testing\RefreshDatabase;
use Illuminate\Support\Carbon;
use Livewire\Livewire;
use Tests\TestCase;

/** The Monitoring page: live values every 5 s, charts, service states and warnings (§7.3, §7.5). */
final class MonitoringPageTest extends TestCase
{
    use RefreshDatabase;

    private FakeAgentClient $agent;

    protected function setUp(): void
    {
        parent::setUp();
        $this->travelTo(Carbon::parse('2026-11-01 03:41:20', 'UTC'));
        $this->agent = new FakeAgentClient;
        $this->app->instance(AgentClient::class, $this->agent);
        $user = User::factory()->create();
        $user->forceFill(['app_authentication_secret' => 'JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP'])->save();
        $this->actingAs($user);
    }

    public function testPageShowsLiveValuesChartsAndServices(): void
    {
        $this->get('/admin/monitoring')->assertOk()->assertSeeInOrder([
            'Monitoring',
            'Load', '0.42', '0.31', '0.27',
            'Memory', '2.8 GiB available of 4 GiB',
            'Disks', '/', '42%',
            'Services', 'nginx.service', 'Active',
        ]);
    }

    /** Disk over 85%, memory under 10% and a service not active warn; exactly at the line doesn't. */
    public function testWarningThresholds(): void
    {
        $at = CarbonImmutable::now();
        $metrics = $this->agent->call(new SystemMetrics)->data;
        $this->assertSame([], MetricWarnings::from($metrics, $at), 'the fake server is healthy');

        $disk = fn (int $used): array => ['mount' => '/srv', 'fs_type' => 'xfs', 'total_bytes' => 1000, 'used_bytes' => $used, 'available_bytes' => 1000 - $used, 'inodes_total' => 0, 'inodes_free' => 0];
        $atLine = array_replace($metrics, [
            'disks' => [$disk(850)],
            'memory' => ['total_bytes' => 1000, 'available_bytes' => 100, 'swap_total_bytes' => 0, 'swap_free_bytes' => 0],
        ]);
        $this->assertSame([], MetricWarnings::from($atLine, $at));

        $over = array_replace($metrics, [
            'disks' => [$disk(851)],
            'memory' => ['total_bytes' => 1000, 'available_bytes' => 99, 'swap_total_bytes' => 0, 'swap_free_bytes' => 0],
            'services' => [
                ['unit' => 'nginx.service', 'active_state' => 'failed', 'unit_file_state' => 'enabled'],
                ['unit' => 'certbot.timer', 'active_state' => 'inactive', 'unit_file_state' => 'enabled'],
                ['unit' => 'zimam-agent.socket', 'active_state' => 'active', 'unit_file_state' => 'enabled'],
            ],
        ]);
        $warnings = MetricWarnings::from($over, $at);
        $this->assertSame(
            ['Disk /srv is 85% full', 'Only 9% of memory is available', 'nginx.service is failed', 'certbot.timer is inactive'],
            array_column($warnings, 'title'),
        );
        $this->assertSame(['warning', 'warning', 'critical', 'warning'], array_column($warnings, 'severity'));

        // The Monitoring page and the dashboard show them.
        $this->agent->changeMetrics(fn (array $m): array => array_replace($m, ['disks' => [$disk(900)]]));
        $this->agent->changeMetrics(function (array $m): array {
            $m['services'][0]['active_state'] = 'failed';

            return $m;
        });
        $this->travel(5)->seconds();
        $this->get('/admin/monitoring')->assertOk()
            ->assertSee('Disk /srv is 90% full')
            ->assertSee('nginx.service is failed');
        $this->get('/admin')->assertOk()
            ->assertSee('One critical, one warning.')
            ->assertSeeInOrder(['nginx.service is failed', 'Critical', 'Disk /srv is 90% full', 'Warning']);
    }

    /** Live values come from a direct system.metrics call every 5 s; CPU and network rates from two calls. */
    public function testLivePolling(): void
    {
        $component = Livewire::test(LiveMetrics::class)
            ->assertSeeHtml('wire:poll.5s')
            ->assertSee('Measuring…');
        $this->assertSame(1, $this->metricsCalls());

        $this->travel(5)->seconds();
        // The fake's counters move on 1000 ticks a call, 150 of them busy, and 250,000 bytes in.
        $component->call('$refresh')->assertSee('15%')->assertSee('48.8 KiB/s');
        $this->assertSame(2, $this->metricsCalls());

        // Services poll too, sharing the same answer within its 4 seconds.
        Livewire::test(ServiceStates::class)->assertSeeHtml('wire:poll.5s')->assertSee('zimam-agent.socket');
        $this->assertSame(2, $this->metricsCalls());
    }

    private function metricsCalls(): int
    {
        return count(array_filter($this->agent->calls(), fn (array $call): bool => $call['command'] === 'system.metrics'));
    }
}
