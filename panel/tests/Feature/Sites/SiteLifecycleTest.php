<?php

namespace Tests\Feature\Sites;

use App\Actions\SubmitAgentCommand;
use App\Agent\AgentClient;
use App\Agent\Exceptions\AgentUnavailable;
use App\Agent\FakeAgentClient;
use App\Filament\Resources\SiteResource\Pages\ListSites;
use App\Filament\Resources\SiteResource\Pages\ViewSite;
use App\Jobs\SubmitAgentJob;
use App\Jobs\TrackAgentJob;
use App\Models\AgentJob;
use App\Models\Site;
use App\Models\User;
use Illuminate\Foundation\Testing\RefreshDatabase;
use Illuminate\Queue\MaxAttemptsExceededException;
use Illuminate\Support\Facades\Queue;
use Livewire\Livewire;
use Tests\TestCase;

/** A site after it's created: the list, the detail page, and the provision outcome (§7.3, §9.2). */
final class SiteLifecycleTest extends TestCase
{
    use RefreshDatabase;

    private FakeAgentClient $agent;

    protected function setUp(): void
    {
        parent::setUp();
        Queue::fake();
        $this->agent = new FakeAgentClient;
        $this->app->instance(AgentClient::class, $this->agent);
        $this->actingAs(User::factory()->create());
    }

    private function site(string $status = Site::PROVISIONING, string $name = 'shop'): Site
    {
        $site = Site::create(['name' => $name, 'type' => 'php', 'php_version' => '8.5', 'status' => $status]);
        $site->domains()->create(['hostname' => "{$name}.example.com", 'is_primary' => true]);
        $site->securitySettings()->create();

        return $site;
    }

    private function provision(Site $site): AgentJob
    {
        return app(SubmitAgentCommand::class)->run($site->provisionCommand(), $site);
    }

    private function trackToTheEnd(AgentJob $job): AgentJob
    {
        for ($i = 0; $i < 10 && ! $job->refresh()->isFinished(); $i++) {
            $track = (new TrackAgentJob($job->fresh()))->withFakeQueueInteractions();
            app()->call([$track, 'handle']);
        }

        return $job;
    }

    public function testListShowsSites(): void
    {
        $sites = [$this->site(Site::ACTIVE), $this->site(Site::FAILED, 'blog')];

        Livewire::test(ListSites::class)
            ->assertCanSeeTableRecords($sites)
            ->assertSee(['shop.example.com', 'blog.example.com', __('zimam.sites.status.active'), __('zimam.sites.status.failed')]);
    }

    public function testOutcomeActivatesSite(): void
    {
        $site = $this->site();
        $this->trackToTheEnd($this->provision($site));

        $site->refresh();
        $this->assertSame(Site::ACTIVE, $site->status);
        $this->assertSame(1, $site->template_version);
        $this->assertNull($site->last_error);
    }

    public function testFailedProvisionMarksFailed(): void
    {
        $this->agent->scriptJob('site.provision', ['running', 'failed'], errorCode: 'internal', message: 'configuration test failed: nginx: [emerg] bogus');
        $site = $this->site();
        $this->trackToTheEnd($this->provision($site));

        $site->refresh();
        $this->assertSame(Site::FAILED, $site->status);
        $this->assertSame('configuration test failed: nginx: [emerg] bogus', $site->last_error);
    }

    /** A job the panel gives up on, never accepted after busy, still fails its site. */
    public function testBusyTimeoutFailsSite(): void
    {
        $site = $this->site();
        $job = new AgentJob(['command' => 'site.provision', 'status' => AgentJob::WAITING, 'params' => $site->provisionCommand()->params()]);
        $job->related()->associate($site);
        $job->save();

        (new SubmitAgentJob($job, $site->provisionCommand()))->failed(new MaxAttemptsExceededException('too many attempts'));

        $this->assertSame(AgentJob::FAILED, $job->refresh()->status);
        $site->refresh();
        $this->assertSame(Site::FAILED, $site->status);
        $this->assertSame(__('zimam.jobs.busy'), $site->last_error);
    }

    /** As does a job the panel stopped tracking. */
    public function testUntrackedJobFailsSite(): void
    {
        $site = $this->site();
        $job = $this->provision($site);

        (new TrackAgentJob($job))->failed(new MaxAttemptsExceededException('too many attempts'));

        $this->assertSame(Site::FAILED, $site->refresh()->status);
        $this->assertSame(__('zimam.jobs.untracked'), $site->last_error);
    }

    public function testFailedSiteShowsRetryAndDelete(): void
    {
        Livewire::test(ViewSite::class, ['record' => $this->site(Site::ACTIVE)->getRouteKey()])
            ->assertActionHidden('retry')
            ->assertActionVisible('delete');

        $failed = $this->site(Site::FAILED, 'blog');
        Livewire::test(ViewSite::class, ['record' => $failed->getRouteKey()])
            ->assertActionVisible('retry')
            ->assertActionEnabled('retry')
            ->assertActionVisible('delete')
            ->assertActionEnabled('delete');
    }

    public function testRetrySubmitsProvisionAgain(): void
    {
        $failed = $this->site(Site::FAILED);
        $failed->update(['last_error' => 'nginx -t failed']);

        Livewire::test(ViewSite::class, ['record' => $failed->getRouteKey()])->callAction('retry');

        $submits = array_values(array_filter($this->agent->calls(), fn (array $c): bool => $c['method'] === 'submit'));
        $this->assertSame([['name' => 'shop', 'domain' => 'shop.example.com', 'type' => 'php', 'php_version' => '8.5']], array_column($submits, 'params'));
        $failed->refresh();
        $this->assertSame(Site::PROVISIONING, $failed->status);
        $this->assertNull($failed->last_error);
    }

    public function testRetryPausedWhileAgentUnreachable(): void
    {
        $failed = $this->site(Site::FAILED);
        $this->agent->respond('agent.version', new AgentUnavailable('connect: no such file'));

        Livewire::test(ViewSite::class, ['record' => $failed->getRouteKey()])->assertActionDisabled('retry');
    }

    public function testViewShowsOverview(): void
    {
        Livewire::test(ViewSite::class, ['record' => $this->site(Site::ACTIVE)->getRouteKey()])
            ->assertOk()
            ->assertSee(['http://shop.example.com', '8.5', __('zimam.sites.status.active')]);
    }
}
