<?php

namespace Tests\Feature\AgentJob;

use App\Agent\AgentClient;
use App\Agent\AgentHealth;
use App\Agent\Exceptions\AgentUnavailable;
use App\Agent\FakeAgentClient;
use App\Models\User;
use Illuminate\Foundation\Testing\RefreshDatabase;
use Illuminate\Support\Carbon;
use Livewire\Livewire;
use Tests\Support\GuardedActionComponent;
use Tests\TestCase;

/** Agent health: an agent.version call cached for 10 seconds (D10). */
final class HealthTest extends TestCase
{
    use RefreshDatabase;

    private FakeAgentClient $agent;

    protected function setUp(): void
    {
        parent::setUp();
        $this->agent = new FakeAgentClient;
        $this->app->instance(AgentClient::class, $this->agent);
        $user = User::factory()->create();
        $user->forceFill(['app_authentication_secret' => 'JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP'])->save();
        $this->actingAs($user);
    }

    public function testBannerWhenUnreachable(): void
    {
        $this->travelTo(Carbon::parse('2026-11-01 03:41:20', 'UTC'));
        $this->get('/admin')->assertOk()->assertDontSee(__('zimam.agent.banner.heading'));

        $this->agentDown();

        $this->get('/admin')
            ->assertOk()
            // Above the page's own content.
            ->assertSeeInOrder([__('zimam.agent.banner.heading'), 'Agent unreachable.'])
            ->assertSee('Its last answer came at 03:41:20.')
            ->assertSee('sudo systemctl status zimam-agent.socket');
    }

    public function testActionsDisabledWhenUnreachable(): void
    {
        Livewire::test(GuardedActionComponent::class)
            ->assertActionEnabled('renew')
            ->callAction('renew')
            ->assertSet('renewed', true);

        $this->agentDown();

        Livewire::test(GuardedActionComponent::class)
            ->assertActionDisabled('renew')
            ->callAction('renew')
            ->assertSet('renewed', false);
    }

    /** Both answers are kept, so a dead agent isn't asked on every page either. */
    public function testCachedFor10Seconds(): void
    {
        $this->freezeSecond();
        $this->assertTrue($this->reachable());
        $this->agent->respond('agent.version', new AgentUnavailable('connect: no such file'));
        $this->travel(9)->seconds();
        $this->assertTrue($this->reachable());
        $this->assertSame(1, $this->versionCalls());

        $this->travel(2)->seconds();
        $this->assertFalse($this->reachable());
        $this->assertSame(2, $this->versionCalls());

        $this->agent->respond('agent.version', ['version' => '0.1.0', 'protocols' => [1], 'build' => 'release']);
        $this->travel(9)->seconds();
        $this->assertFalse($this->reachable());
        $this->assertSame(2, $this->versionCalls());

        $this->travel(2)->seconds();
        $this->assertTrue($this->reachable());
        $this->assertSame(3, $this->versionCalls());
    }

    private function agentDown(): void
    {
        $this->agent->respond('agent.version', new AgentUnavailable('connect: no such file'));
        $this->agent->respond('system.status', new AgentUnavailable('connect: no such file'));
        $this->travel(11)->seconds();
        // A new request, as on a server: AgentHealth keeps its answer for one request.
        $this->app->forgetScopedInstances();
    }

    /** A fresh AgentHealth, as each request gets. */
    private function reachable(): bool
    {
        $this->app->forgetScopedInstances();

        return app(AgentHealth::class)->isReachable();
    }

    private function versionCalls(): int
    {
        return count(array_filter($this->agent->calls(), fn (array $call): bool => $call['command'] === 'agent.version'));
    }
}
