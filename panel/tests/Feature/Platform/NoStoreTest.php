<?php

namespace Tests\Feature\Platform;

use App\Agent\AgentClient;
use App\Agent\FakeAgentClient;
use App\Models\User;
use Illuminate\Foundation\Testing\RefreshDatabase;
use Tests\TestCase;

/** The browser must not keep the panel's pages: the back button would restore a revealed secret. */
final class NoStoreTest extends TestCase
{
    use RefreshDatabase;

    public function testPanelPagesAreNotStored(): void
    {
        $this->app->instance(AgentClient::class, new FakeAgentClient);
        $user = User::factory()->create();
        $user->forceFill(['app_authentication_secret' => 'JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP'])->save();

        $this->get('/admin/login')->assertOk()->assertHeader('Cache-Control');
        $this->assertStringContainsString('no-store', $this->get('/admin/login')->headers->get('Cache-Control'));

        $this->actingAs($user);
        foreach (['/admin', '/admin/sites', '/admin/monitoring'] as $url) {
            $this->assertStringContainsString('no-store', $this->get($url)->assertOk()->headers->get('Cache-Control'), $url);
        }
    }
}
