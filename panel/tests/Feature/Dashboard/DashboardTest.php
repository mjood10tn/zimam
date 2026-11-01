<?php

namespace Tests\Feature\Dashboard;

use App\Agent\AgentClient;
use App\Agent\Commands\SystemStatus;
use App\Agent\Exceptions\AgentUnavailable;
use App\Agent\FakeAgentClient;
use App\Models\User;
use Illuminate\Foundation\Testing\RefreshDatabase;
use Illuminate\Support\Carbon;
use Tests\TestCase;

/**
 * The dashboard reads like a bulletin (docs/design/dashboard-brief.md):
 * a headline counting the warnings in force, one sentence naming them,
 * the issued time, then the warnings and the server summary (§7.3, §7.5).
 */
final class DashboardTest extends TestCase
{
    use RefreshDatabase;

    private FakeAgentClient $agent;

    protected function setUp(): void
    {
        parent::setUp();
        $this->travelTo(Carbon::parse('2026-11-01 03:41:20', 'UTC'));
        $this->useAgent(new FakeAgentClient);
        $user = User::factory()->create();
        $user->forceFill(['app_authentication_secret' => 'JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP'])->save();
        $this->actingAs($user);
    }

    public function testShowsStatus(): void
    {
        $this->useAgent(FakeAgentClient::demo());

        $this->get('/admin')->assertOk()->assertSeeInOrder([
            'Issued 03:41:20 · Next 03:41:30',
            'Server',
            'Agent', 'zimam-agent 0.1.0, protocol 1',
            'Nginx', '1.28.3',
            'PHP', '8.3, 8.4 (default), 8.5 (panel)',
            'Panel domain', 'panel.example.com',
            'Certificates', 'Let’s Encrypt',
            'Reboot', 'Required',
            'Updates', '12 pending, 3 for security, checked 03:16',
        ]);

        $status = $this->statusWith([
            'nginx_version' => '',
            'php' => ['installed' => [['version' => '8.5', 'fpm_active' => true], ['version' => '8.3', 'fpm_active' => true]], 'default' => '8.5', 'panel' => '8.5', 'available' => []],
            'acme_directory' => 'https://acme-staging-v02.api.letsencrypt.org/directory',
            'reboot_required' => false,
            'updates' => ['total' => null, 'security' => null, 'checked_at' => null],
        ]);
        $this->agent->respond('system.status', $status);
        $this->travel(11)->seconds();

        $this->get('/admin')->assertOk()->assertSeeInOrder([
            'Nginx', 'Version unknown',
            'PHP', '8.3, 8.5 (default, panel)',
            'Certificates', 'Let’s Encrypt staging',
            'Reboot', 'Not required',
            'Updates', 'Not checked yet',
        ]);
    }

    /** The last good status stays, with its time, while the agent is down (§12). */
    public function testShowsCachedStatusWhenAgentDown(): void
    {
        $this->useAgent(FakeAgentClient::demo());
        $this->get('/admin')->assertOk()->assertSee('Two warnings in force.');

        $this->travel(3)->minutes();
        $this->agentDown();

        $this->get('/admin')->assertOk()
            ->assertSee('Agent unreachable.')
            ->assertSee('Last report at 03:41:20: reboot required. 3 security updates pending.')
            ->assertSee('Waiting for the agent')
            ->assertSee('As last reported at 03:41:20.')
            ->assertSee('1.28.3')
            ->assertSee('12 pending, 3 for security')
            ->assertDontSee('Issued 03:44');
        $this->assertSame(1, $this->statusCalls(), 'asked a dead agent for its status');

        $this->flushCache();
        $this->get('/admin')->assertOk()
            ->assertSee('Agent unreachable.')
            ->assertSee('No report from the agent yet.')
            ->assertDontSee('1.28.3');
    }

    public function testWarnings(): void
    {
        $this->get('/admin')->assertOk()
            ->assertSee('No warnings.')
            ->assertSee('Nothing needs your attention.')
            ->assertSee('None pending, checked 03:31')
            ->assertDontSee('Reboot required')
            ->assertDontSee('in force');

        $this->agent->respond('system.status', $this->statusWith(['reboot_required' => true]));
        $this->travel(11)->seconds();
        $this->get('/admin')->assertOk()
            ->assertSee('One warning in force.')
            ->assertSee('Reboot required.');

        $this->useAgent(FakeAgentClient::demo());
        $this->travel(11)->seconds();
        $this->get('/admin')->assertOk()
            ->assertSee('Two warnings in force.')
            ->assertSee('Reboot required. 3 security updates pending.')
            ->assertSeeInOrder(['Reboot required', 'Warning', '3 security updates pending', 'Warning', 'checked 03:16']);

        $this->agentDown();
        $this->get('/admin')->assertOk()
            ->assertSee('Agent unreachable.')
            ->assertSee(__('zimam.agent.banner.heading'));
    }

    /** A page left open refreshes both widgets, so the server facts never look fresher than the bulletin. */
    public function testBothWidgetsRefreshEvery10Seconds(): void
    {
        $this->assertSame(2, substr_count($this->get('/admin')->assertOk()->getContent(), 'wire:poll.10s'));
    }

    private function useAgent(FakeAgentClient $agent): void
    {
        $this->agent = $agent;
        $this->app->instance(AgentClient::class, $agent);
    }

    /** @param array<string, mixed> $changes */
    private function statusWith(array $changes): array
    {
        return array_replace((new FakeAgentClient)->call(new SystemStatus)->data, $changes);
    }

    private function agentDown(): void
    {
        $this->agent->respond('agent.version', new AgentUnavailable('connect: no such file'));
        $this->agent->respond('system.status', new AgentUnavailable('connect: no such file'));
        $this->travel(11)->seconds();
        $this->app->forgetScopedInstances();
    }

    private function flushCache(): void
    {
        cache()->flush();
        $this->app->forgetScopedInstances();
    }

    private function statusCalls(): int
    {
        return count(array_filter($this->agent->calls(), fn (array $call): bool => $call['command'] === 'system.status'));
    }
}
