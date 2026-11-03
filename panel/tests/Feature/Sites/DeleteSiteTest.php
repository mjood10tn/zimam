<?php

namespace Tests\Feature\Sites;

use App\Agent\AgentClient;
use App\Agent\Exceptions\AgentConflict;
use App\Agent\Exceptions\AgentNotFound;
use App\Agent\FakeAgentClient;
use App\Filament\Resources\SiteResource;
use App\Filament\Resources\SiteResource\Pages\ViewSite;
use App\Jobs\TrackAgentJob;
use App\Models\AgentJob;
use App\Models\Domain;
use App\Models\Site;
use App\Models\SiteSecuritySetting;
use App\Models\User;
use Illuminate\Foundation\Testing\RefreshDatabase;
use Illuminate\Support\Facades\Queue;
use Livewire\Livewire;
use Tests\TestCase;

/** Deleting a site (§9.8): password first, then site.destroy, then the rows go with its outcome. */
final class DeleteSiteTest extends TestCase
{
    use RefreshDatabase;

    private FakeAgentClient $agent;

    protected function setUp(): void
    {
        parent::setUp();
        Queue::fake();
        $this->agent = new FakeAgentClient;
        $this->app->instance(AgentClient::class, $this->agent);
        $this->actingAs(User::factory()->create(['password' => 'the right password']));
    }

    private function site(string $status = Site::ACTIVE): Site
    {
        $site = Site::create(['name' => 'shop', 'type' => 'static', 'status' => $status]);
        $site->domains()->create(['hostname' => 'shop.example.com', 'is_primary' => true]);
        $site->securitySettings()->create();

        return $site;
    }

    /** @return list<array<string, mixed>> */
    private function destroys(): array
    {
        return array_values(array_column(array_filter($this->agent->calls(), fn (array $c): bool => $c['method'] === 'submit' && $c['command'] === 'site.destroy'), 'params'));
    }

    private function trackToTheEnd(AgentJob $job): void
    {
        for ($i = 0; $i < 10 && ! $job->refresh()->isFinished(); $i++) {
            $track = (new TrackAgentJob($job->fresh()))->withFakeQueueInteractions();
            app()->call([$track, 'handle']);
        }
    }

    public function testDeleteRequiresPassword(): void
    {
        $site = $this->site();

        Livewire::test(ViewSite::class, ['record' => $site->getRouteKey()])
            ->callAction('delete', ['current_password' => 'not the password'])
            ->assertHasActionErrors(['current_password']);

        $this->assertSame([], $this->destroys());
        $this->assertSame(Site::ACTIVE, $site->refresh()->status);
    }

    public function testDeleteSubmits(): void
    {
        $site = $this->site();

        Livewire::test(ViewSite::class, ['record' => $site->getRouteKey()])
            ->callAction('delete', ['current_password' => 'the right password'])
            ->assertHasNoActionErrors()
            ->assertNotified(__('zimam.sites.delete.submitted', ['name' => 'shop']))
            ->assertRedirect(SiteResource::getUrl('index'));

        $this->assertSame([['name' => 'shop']], $this->destroys());
        $this->assertSame(Site::DELETING, $site->refresh()->status);
        $this->assertTrue(AgentJob::sole()->related->is($site));
    }

    public function testOutcomeDeletesRows(): void
    {
        $site = $this->site();
        Livewire::test(ViewSite::class, ['record' => $site->getRouteKey()])->callAction('delete', ['current_password' => 'the right password']);

        $this->trackToTheEnd(AgentJob::sole());

        $this->assertSame(0, Site::count());
        $this->assertSame(0, Domain::count());
        $this->assertSame(0, SiteSecuritySetting::count());
        $this->assertSame(AgentJob::SUCCEEDED, AgentJob::sole()->status);
    }

    /** A destroy that failed leaves the site failed with the reason, so Delete can run again. */
    public function testFailedDestroyMarksFailed(): void
    {
        $this->agent->scriptJob('site.destroy', ['running', 'failed'], errorCode: 'internal', message: '/home/s_shop isn’t empty after the helper emptied it');
        $site = $this->site();
        Livewire::test(ViewSite::class, ['record' => $site->getRouteKey()])->callAction('delete', ['current_password' => 'the right password']);

        $this->trackToTheEnd(AgentJob::sole());

        $site->refresh();
        $this->assertSame(Site::FAILED, $site->status);
        $this->assertSame('/home/s_shop isn’t empty after the helper emptied it', $site->last_error);
        Livewire::test(ViewSite::class, ['record' => $site->getRouteKey()])->assertActionVisible('delete')->assertActionEnabled('delete');
    }

    /** Nothing of the site is on the server, so the panel's rows go at once. */
    public function testNotFoundRemovesRows(): void
    {
        $this->agent->respond('site.destroy', new AgentNotFound("Site shop doesn't exist."));
        $site = $this->site(Site::FAILED);

        Livewire::test(ViewSite::class, ['record' => $site->getRouteKey()])
            ->callAction('delete', ['current_password' => 'the right password'])
            ->assertRedirect(SiteResource::getUrl('index'));

        $this->assertSame(0, Site::count());
        $this->assertSame(0, AgentJob::count());
    }

    public function testRefusalRestoresStatus(): void
    {
        $this->agent->respond('site.destroy', new AgentConflict('site:shop already has a job running'));
        $site = $this->site();

        Livewire::test(ViewSite::class, ['record' => $site->getRouteKey()])
            ->callAction('delete', ['current_password' => 'the right password'])
            ->assertNotified('site:shop already has a job running');

        $this->assertSame(Site::ACTIVE, $site->refresh()->status);
    }

    public function testDeleteOnlyForActiveOrFailedSites(): void
    {
        foreach ([Site::PROVISIONING, Site::DELETING] as $status) {
            Site::query()->delete();
            Livewire::test(ViewSite::class, ['record' => $this->site($status)->getRouteKey()])->assertActionHidden('delete');
        }
        foreach ([Site::ACTIVE, Site::FAILED] as $status) {
            Site::query()->delete();
            Livewire::test(ViewSite::class, ['record' => $this->site($status)->getRouteKey()])->assertActionVisible('delete')->assertActionEnabled('delete');
        }
    }
}
