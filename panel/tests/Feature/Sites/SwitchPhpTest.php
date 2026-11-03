<?php

namespace Tests\Feature\Sites;

use App\Agent\AgentClient;
use App\Agent\AgentCommand;
use App\Agent\Commands\SitePhp;
use App\Agent\Commands\SiteProvision;
use App\Agent\Commands\SystemStatus;
use App\Agent\Exceptions\AgentNotFound;
use App\Agent\FakeAgentClient;
use App\Filament\Resources\SiteResource\Pages\ViewSite;
use App\Jobs\TrackAgentJob;
use App\Models\AgentJob;
use App\Models\Site;
use App\Models\User;
use Illuminate\Foundation\Testing\RefreshDatabase;
use Illuminate\Support\Facades\Queue;
use Livewire\Livewire;
use Tests\TestCase;

/** Switching a PHP site to another version (§9.5). */
final class SwitchPhpTest extends TestCase
{
    use RefreshDatabase;

    private FakeAgentClient $agent;

    protected function setUp(): void
    {
        parent::setUp();
        Queue::fake();
        $this->agent = (new FakeAgentClient)->changeServer(function (array $status): array {
            $status['php']['installed'] = [
                ['version' => '8.2', 'fpm_active' => false, 'in_use' => false],
                ['version' => '8.3', 'fpm_active' => false, 'in_use' => false],
                ['version' => '8.4', 'fpm_active' => true, 'in_use' => true],
                ['version' => '8.5', 'fpm_active' => true, 'in_use' => true],
            ];

            return $status;
        });
        $this->app->instance(AgentClient::class, $this->agent);
        $this->actingAs(User::factory()->create());
    }

    private function site(string $type = Site::PHP, string $status = Site::ACTIVE): Site
    {
        $site = Site::create(['name' => 'shop', 'type' => $type, 'php_version' => $type === Site::PHP ? '8.4' : null, 'status' => $status]);
        $site->domains()->create(['hostname' => 'shop.example.com', 'is_primary' => true]);
        $site->securitySettings()->create();

        return $site;
    }

    /** @return list<array<string, mixed>> */
    private function switches(): array
    {
        return array_values(array_column(array_filter($this->agent->calls(), fn (array $c): bool => $c['method'] === 'submit' && $c['command'] === 'site.php'), 'params'));
    }

    private function trackToTheEnd(AgentJob $job): AgentJob
    {
        for ($i = 0; $i < 10 && ! $job->refresh()->isFinished(); $i++) {
            $track = (new TrackAgentJob($job->fresh()))->withFakeQueueInteractions();
            app()->call([$track, 'handle']);
        }

        return $job;
    }

    public function testSwitchSubmits(): void
    {
        $site = $this->site();

        Livewire::test(ViewSite::class, ['record' => $site->getRouteKey()])
            ->callAction('switchPhp', ['php_version' => '8.5'])
            ->assertHasNoActionErrors()
            ->assertNotified(__('zimam.sites.switch.submitted', ['name' => 'shop', 'version' => '8.5']));

        $this->assertSame([['name' => 'shop', 'php_version' => '8.5']], $this->switches());
        $job = AgentJob::sole();
        $this->assertSame('site.php', $job->command);
        $this->assertTrue($job->related->is($site));
        // The site keeps serving on 8.4 until the switch is done.
        $site->refresh();
        $this->assertSame(Site::ACTIVE, $site->status);
        $this->assertSame('8.4', $site->php_version);
    }

    /** Installed, supported versions other than the site's own. */
    public function testSwitchOffersOtherSupportedVersions(): void
    {
        $this->agent->changeServer(function (array $status): array {
            $status['php']['available'][0]['supported'] = false; // 8.2

            return $status;
        });
        $site = $this->site();

        $page = Livewire::test(ViewSite::class, ['record' => $site->getRouteKey()])->mountAction('switchPhp');

        $this->assertSame(['8.3', '8.5'], array_keys($page->instance()->switchPhpOptions()));
    }

    public function testOutcomeUpdatesVersion(): void
    {
        $site = $this->site();
        Livewire::test(ViewSite::class, ['record' => $site->getRouteKey()])->callAction('switchPhp', ['php_version' => '8.5']);

        $this->trackToTheEnd(AgentJob::sole());

        $site->refresh();
        $this->assertSame('8.5', $site->php_version);
        $this->assertSame(Site::ACTIVE, $site->status);
        $this->assertNull($site->last_error);
    }

    /** A failed switch leaves the site serving on its old version: still active, with the reason shown. */
    public function testFailedSwitchKeepsSiteActive(): void
    {
        $this->agent->scriptJob('site.php', ['running', 'failed'], errorCode: 'internal', message: 'configuration test failed: nginx: [emerg] bogus');
        $site = $this->site();
        Livewire::test(ViewSite::class, ['record' => $site->getRouteKey()])->callAction('switchPhp', ['php_version' => '8.5']);

        $this->trackToTheEnd(AgentJob::sole());

        $site->refresh();
        $this->assertSame(Site::ACTIVE, $site->status);
        $this->assertSame('8.4', $site->php_version);
        $this->assertSame('configuration test failed: nginx: [emerg] bogus', $site->last_error);
    }

    public function testRefusalIsShown(): void
    {
        $this->agent->respond('site.php', new AgentNotFound("PHP 8.5 isn't installed."));
        $site = $this->site();

        Livewire::test(ViewSite::class, ['record' => $site->getRouteKey()])
            ->callAction('switchPhp', ['php_version' => '8.5'])
            ->assertNotified("PHP 8.5 isn't installed.");

        $this->assertSame(0, AgentJob::count());
    }

    public function testSwitchOnlyForActivePhpSites(): void
    {
        Livewire::test(ViewSite::class, ['record' => $this->site(Site::STATIC)->getRouteKey()])->assertActionHidden('switchPhp');
        Site::query()->delete();
        Livewire::test(ViewSite::class, ['record' => $this->site(Site::PHP, Site::FAILED)->getRouteKey()])->assertActionHidden('switchPhp');
    }

    /** The fake moves in_use as the agent does, so the PHP versions page offers Uninstall for the version left behind. */
    public function testFakeMovesVersionInUse(): void
    {
        $run = function (AgentCommand $command): void {
            $id = $this->agent->submit($command)->id;
            while (! $this->agent->status($id)->isFinished());
        };
        $run(new SiteProvision('blog', 'blog.example.com', 'php', '8.3'));
        $run(new SitePhp('blog', '8.2'));

        $inUse = array_column($this->agent->call(new SystemStatus)->data['php']['installed'], 'in_use', 'version');
        $this->assertSame(['8.2' => true, '8.3' => false, '8.4' => true, '8.5' => true], $inUse);
    }

    /** One job at a time per site: the action waits for the last one to end. */
    public function testSwitchDisabledWhileSiteJobRuns(): void
    {
        $site = $this->site();
        Livewire::test(ViewSite::class, ['record' => $site->getRouteKey()])->callAction('switchPhp', ['php_version' => '8.5']);

        Livewire::test(ViewSite::class, ['record' => $site->getRouteKey()])->assertActionDisabled('switchPhp');
    }
}
