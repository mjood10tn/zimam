<?php

namespace Tests\Feature\Platform;

use App\Agent\AgentClient;
use App\Agent\FakeAgentClient;
use App\Http\Middleware\NoStorePanelPages;
use App\Models\User;
use Illuminate\Foundation\Testing\RefreshDatabase;
use Livewire\Livewire;
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

    /**
     * A Livewire update is the response that carries a revealed password, so
     * it must not be stored either: the page's middleware doesn't run on an
     * update, only the persistent ones do.
     */
    public function testLivewireUpdatesAreNotStored(): void
    {
        $this->app->instance(AgentClient::class, new FakeAgentClient);
        $user = User::factory()->create();
        $user->forceFill(['app_authentication_secret' => 'JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP'])->save();
        $this->actingAs($user);
        preg_match_all('/wire:snapshot="([^"]*)"/', $this->get('/admin')->assertOk()->getContent(), $matches);
        $snapshot = null;
        foreach ($matches[1] as $attribute) {
            $candidate = html_entity_decode($attribute, ENT_QUOTES);
            if ((json_decode($candidate, true)['memo']['name'] ?? null) === 'App\Filament\Pages\Dashboard') {
                $snapshot = $candidate;
            }
        }
        $this->assertNotNull($snapshot, 'no Dashboard snapshot on the page');

        // Each php-fpm request starts clean; in one test process Livewire would skip middleware it already ran.
        Livewire::flushState();
        $response = $this->withHeader('X-Livewire', 'true')->postJson(Livewire::getUpdateUri(), [
            'components' => [['snapshot' => $snapshot, 'updates' => [], 'calls' => [['path' => '', 'method' => '$refresh', 'params' => []]]]],
        ])->assertOk();

        $this->assertStringContainsString('no-store', (string) $response->headers->get('Cache-Control'));
        // Not left to whatever else sets the header: the middleware itself re-runs on updates.
        $this->assertContains(NoStorePanelPages::class, Livewire::getPersistentMiddleware());
    }
}
