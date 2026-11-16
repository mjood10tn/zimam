<?php

namespace Tests\Feature\Deployments;

use App\Actions\StartDeployment;
use App\Filament\Resources\SiteResource\Pages\DeploymentsTab;
use App\Models\AgentJob;
use App\Models\Deployment;
use App\Models\Site;
use Livewire\Livewire;

/** The kept releases (site.releases) and Roll back (site.rollback), behind password confirmation. */
final class RollbackTest extends DeploymentsTestCase
{
    private const OLD = '20261110090000-1111111';

    private const LIVE = '20261112090000-2222222';

    private function deployedTwice(): Site
    {
        $site = $this->site();
        $this->connected($site);
        $this->agent->release('shop', self::OLD, str_repeat('1', 40))
            ->release('shop', self::LIVE, str_repeat('2', 40), active: true);

        return $site;
    }

    public function testReleasesListed(): void
    {
        $site = $this->deployedTwice();

        Livewire::test(DeploymentsTab::class, ['site' => $site])
            ->assertSee(self::OLD)
            ->assertSee(self::LIVE)
            ->assertSee(__('zimam.deployments.releases.live'))
            ->assertActionVisible('rollback', ['release' => self::OLD])
            ->assertActionHidden('rollback', ['release' => self::LIVE]);
    }

    /** A site with no connected repository asks nothing about releases. */
    public function testNoReleasesReadWithoutRepository(): void
    {
        Livewire::test(DeploymentsTab::class, ['site' => $this->site()]);

        $this->assertSame([], array_filter($this->agent->calls(), fn (array $c): bool => $c['command'] === 'site.releases'));
    }

    /**
     * The releases are read when the tab opens, not again on each 5-second
     * poll while a job runs, and once more when it has ended.
     */
    public function testReleasesNotReadOnEveryPoll(): void
    {
        $site = $this->deployedTwice();
        app(StartDeployment::class)->run($site, Deployment::MANUAL);
        $reads = fn (): int => count(array_filter($this->agent->calls(), fn (array $c): bool => $c['command'] === 'site.releases'));

        $tab = Livewire::test(DeploymentsTab::class, ['site' => $site])->assertSee(self::OLD);
        $this->assertSame(1, $reads());
        $tab->call('$refresh')->call('$refresh')->assertSee(self::OLD);
        $this->assertSame(1, $reads());

        $this->trackToTheEnd(AgentJob::sole());
        $tab->call('$refresh');
        $this->assertSame(2, $reads());
        $tab->call('$refresh')->call('$refresh');
        $this->assertSame(2, $reads());
    }

    public function testRollbackNeedsPasswordConfirmation(): void
    {
        $site = $this->deployedTwice();

        Livewire::test(DeploymentsTab::class, ['site' => $site])
            ->callAction('rollback', ['current_password' => 'not the password'], ['release' => self::OLD])
            ->assertHasActionErrors(['current_password']);
        Livewire::test(DeploymentsTab::class, ['site' => $site])
            ->callAction('rollback', ['current_password' => ''], ['release' => self::OLD])
            ->assertHasActionErrors(['current_password']);

        $this->assertSame([], $this->submitted('site.rollback'));
        $this->assertSame(0, AgentJob::count());
    }

    public function testRollbackSubmitsRelease(): void
    {
        $site = $this->deployedTwice();

        Livewire::test(DeploymentsTab::class, ['site' => $site])
            ->callAction('rollback', ['current_password' => self::PASSWORD], ['release' => self::OLD])
            ->assertHasNoActionErrors()
            ->assertNotified(__('zimam.deployments.rollback.submitted', ['name' => 'shop', 'release' => self::OLD]));

        $this->assertSame([['name' => 'shop', 'release' => self::OLD]], $this->submitted('site.rollback'));
        $job = AgentJob::sole();
        $this->assertSame('site.rollback', $job->command);
        $this->assertTrue($job->related->is($site));
        $this->assertSame(300, $job->runtime_max_seconds);

        $this->trackToTheEnd($job);
        $this->assertSame(AgentJob::SUCCEEDED, $job->status);
        Livewire::test(DeploymentsTab::class, ['site' => $site->fresh()])
            ->assertActionHidden('rollback', ['release' => self::OLD])
            ->assertActionVisible('rollback', ['release' => self::LIVE]);
    }

    /** A release the panel never listed is refused before the agent is asked. */
    public function testRollbackOnlyToListedRelease(): void
    {
        $site = $this->deployedTwice();

        Livewire::test(DeploymentsTab::class, ['site' => $site])
            ->assertActionHidden('rollback', ['release' => '../initial'])
            ->assertActionHidden('rollback', ['release' => '20261101090000-3333333']);

        $this->assertSame([], $this->submitted('site.rollback'));
    }

    public function testFailedRollbackKeepsReason(): void
    {
        $site = $this->deployedTwice();
        $this->agent->scriptJob('site.rollback', ['running', 'failed'], message: 'swapping current failed');

        Livewire::test(DeploymentsTab::class, ['site' => $site])
            ->callAction('rollback', ['current_password' => self::PASSWORD], ['release' => self::OLD]);
        $this->trackToTheEnd(AgentJob::sole());

        $this->assertSame('swapping current failed', $site->fresh()->last_error);
    }
}
