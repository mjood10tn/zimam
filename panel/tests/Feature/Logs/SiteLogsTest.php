<?php

namespace Tests\Feature\Logs;

use App\Agent\AgentClient;
use App\Agent\FakeAgentClient;
use App\Livewire\LogViewer;
use App\Livewire\SiteLogs;
use App\Models\Site;
use App\Models\User;
use Illuminate\Foundation\Testing\RefreshDatabase;
use Livewire\Features\SupportLockedProperties\CannotUpdateLockedPropertyException;
use Livewire\Livewire;
use Tests\TestCase;

/** A site's Logs tab (§11.1): its Nginx and PHP logs, and Laravel's log for Laravel sites (R02-D8). */
final class SiteLogsTest extends TestCase
{
    use RefreshDatabase;

    private FakeAgentClient $agent;

    protected function setUp(): void
    {
        parent::setUp();
        $this->agent = new FakeAgentClient;
        $this->app->instance(AgentClient::class, $this->agent);
        $this->actingAs(User::factory()->create());
    }

    private function site(string $type, string $name): Site
    {
        return Site::create(['name' => $name, 'type' => $type, 'php_version' => $type === Site::STATIC ? null : '8.5', 'status' => Site::ACTIVE]);
    }

    /** @return list<string> the sources logs.read was asked for */
    private function readSources(): array
    {
        return array_values(array_column(array_column(array_filter($this->agent->calls(), fn (array $c): bool => $c['command'] === 'logs.read'), 'params'), 'source'));
    }

    public function testLaravelLogListedForLaravelSites(): void
    {
        $site = $this->site(Site::LARAVEL, 'blog');
        $this->agent->setLog('site.laravel', ['[2026-11-16 12:00:00] production.ERROR: boom'], 'blog');

        Livewire::test(SiteLogs::class, ['site' => $site])
            ->assertSee(__('zimam.logs.sources.site.laravel'))
            ->assertSeeHtml('value="site.laravel"')
            ->set('source', 'site.laravel')
            ->assertSet('source', 'site.laravel');
        Livewire::test(LogViewer::class, ['source' => 'site.laravel', 'site' => 'blog'])
            ->assertSee('production.ERROR: boom');

        $this->assertContains('site.laravel', $this->readSources());
        $this->assertSame(['source' => 'site.laravel', 'name' => 'blog', 'direction' => 'back', 'limit' => 200], array_values(array_filter($this->agent->calls(), fn (array $c): bool => $c['command'] === 'logs.read' && $c['params']['source'] === 'site.laravel'))[0]['params']);
    }

    public function testLaravelLogNotListedForStaticSites(): void
    {
        foreach ([Site::STATIC => 'docs', Site::PHP => 'shop'] as $type => $name) {
            Livewire::test(SiteLogs::class, ['site' => $this->site($type, $name)])
                ->assertDontSeeHtml('value="site.laravel"')
                ->assertDontSee(__('zimam.logs.sources.site.laravel'))
                ->set('source', 'site.laravel')
                ->assertSet('source', 'site.nginx-access');
        }

        $this->assertNotContains('site.laravel', $this->readSources());
    }

    public function testSiteIsLocked(): void
    {
        $this->expectException(CannotUpdateLockedPropertyException::class);

        Livewire::test(SiteLogs::class, ['site' => $this->site(Site::STATIC, 'docs')])->set('site', null);
    }
}
