<?php

namespace Tests\Feature\Schedule;

use App\Agent\Commands\SiteCron;
use App\Filament\Resources\SiteResource\Pages\ScheduleTab;
use App\Filament\Resources\SiteResource\Pages\ViewSite;
use App\Livewire\LogViewer;
use App\Livewire\SiteLogs;
use App\Models\AgentJob;
use App\Models\Site;
use App\Models\SiteCronJob;
use Livewire\Features\SupportTesting\Testable;
use Livewire\Livewire;
use Tests\Feature\Databases\DatabasesTestCase;

/**
 * A site's Schedule tab (v0.4.0, R04-D5): the Laravel scheduler's switch and
 * the owner's cron jobs, saved as one whole list through site.cron, the next
 * runs in the server's zone (ruling R7) and the cron log.
 */
final class ScheduleTabTest extends DatabasesTestCase
{
    private function tab(Site $site): Testable
    {
        return Livewire::test(ScheduleTab::class, ['site' => $site]);
    }

    /** A stored job, as a saved list leaves it. */
    private function job(Site $site, string $id, array $attributes = []): SiteCronJob
    {
        return $site->cronJobs()->create(array_replace([
            'job_id' => $id,
            'schedule' => '*/5 * * * *',
            'command' => 'php artisan report',
            'enabled' => true,
            'status' => SiteCronJob::ACTIVE,
        ], $attributes));
    }

    /** @return array<string, mixed> the Add form */
    private static function form(array $over = []): array
    {
        return array_replace(['job_id' => 'report', 'schedule' => '0 3 * * mon', 'command' => 'php artisan report', 'enabled' => true], $over);
    }

    public function testSchedulerToggleSubmits(): void
    {
        $site = $this->site();
        $this->job($site, 'report', ['enabled' => false]);

        $this->tab($site)->callAction('scheduler')->assertNotified(__('zimam.schedule.scheduler.submitted_on', ['name' => 'shop']));

        // Still off until the agent has applied it.
        $this->assertFalse($site->refresh()->scheduler_enabled);
        $this->assertSame([[
            'name' => 'shop',
            'scheduler' => true,
            'jobs' => [['id' => 'report', 'schedule' => '*/5 * * * *', 'command' => 'php artisan report', 'enabled' => false]],
        ]], $this->submitted('site.cron'));
        $this->tab($site)->assertActionDisabled('scheduler');

        $this->trackToTheEnd(AgentJob::sole());
        $this->assertTrue($site->refresh()->scheduler_enabled);

        $this->tab($site)->callAction('scheduler')->assertNotified(__('zimam.schedule.scheduler.submitted_off', ['name' => 'shop']));
        $this->trackToTheEnd(AgentJob::query()->latest('id')->first());

        $this->assertSame([true, false], array_column($this->submitted('site.cron'), 'scheduler'));
        $this->assertFalse($site->refresh()->scheduler_enabled);
    }

    public function testFailedSchedulerChangeKeepsTheOldState(): void
    {
        $site = $this->site();
        $this->agent->scriptJob('site.cron', ['running', 'failed'], errorCode: 'internal', message: 'cron file check failed');

        $this->tab($site)->callAction('scheduler');
        $this->trackToTheEnd(AgentJob::sole());

        $this->assertFalse($site->refresh()->scheduler_enabled);
        $this->assertSame('cron file check failed', $site->last_error);
    }

    public function testStaticSiteHasJobsButNoScheduler(): void
    {
        $static = $this->site('docs', Site::STATIC);

        $this->tab($static)
            ->assertSee(__('zimam.schedule.scheduler.static'))
            ->assertActionHidden('scheduler')
            ->assertActionVisible('add')
            ->callAction('add', self::form())
            ->assertHasNoActionErrors();

        $this->assertFalse($this->submitted('site.cron')[0]['scheduler']);
    }

    public function testCronJobSubmitsWholeList(): void
    {
        $site = $this->site();
        $site->update(['scheduler_enabled' => true]);
        $this->job($site, 'backup', ['schedule' => '@daily', 'command' => 'php artisan backup:run']);

        $this->tab($site)
            ->callAction('add', self::form())
            ->assertHasNoActionErrors()
            ->assertNotified(__('zimam.schedule.submitted', ['name' => 'shop']));

        $this->assertSame([[
            'name' => 'shop',
            'scheduler' => true,
            'jobs' => [
                ['id' => 'backup', 'schedule' => '@daily', 'command' => 'php artisan backup:run', 'enabled' => true],
                ['id' => 'report', 'schedule' => '0 3 * * mon', 'command' => 'php artisan report', 'enabled' => true],
            ],
        ]], $this->submitted('site.cron'));
        $this->assertSame([SiteCronJob::APPLYING], $site->cronJobs()->distinct()->pluck('status')->all());

        $this->trackToTheEnd(AgentJob::sole());
        $this->assertSame([SiteCronJob::ACTIVE], $site->cronJobs()->distinct()->pluck('status')->all());
    }

    public function testEditAndDeleteSubmitWholeList(): void
    {
        $site = $this->site();
        $backup = $this->job($site, 'backup');
        $report = $this->job($site, 'report');

        $this->tab($site)->callTableAction('edit', $report, self::form(['schedule' => '*/10 * * * *', 'enabled' => false]))->assertHasNoTableActionErrors();
        $this->trackToTheEnd(AgentJob::sole());
        $this->assertFalse($report->refresh()->enabled);

        $this->tab($site)->callTableAction('delete', $backup, ['current_password' => 'not the password'])->assertHasTableActionErrors(['current_password']);
        $this->tab($site)->callTableAction('delete', $backup, ['current_password' => self::PASSWORD])->assertHasNoTableActionErrors();

        $lists = array_column($this->submitted('site.cron'), 'jobs');
        $this->assertSame(['backup', 'report'], array_column($lists[0], 'id'));
        $this->assertSame(['*/10 * * * *', false], [$lists[0][1]['schedule'], $lists[0][1]['enabled']]);
        $this->assertSame(['report'], array_column($lists[1], 'id'));
        // The row stays, marked removing, until the agent has removed it.
        $this->assertSame(SiteCronJob::REMOVING, $backup->refresh()->status);
        $this->tab($site)->assertSee(__('zimam.schedule.status.removing'));

        $this->trackToTheEnd(AgentJob::query()->latest('id')->first());

        $this->assertSame(['report'], $site->cronJobs()->pluck('job_id')->all());
    }

    /** A removal that fails leaves the job, which may still run, on the tab to remove again. */
    public function testFailedDeleteKeepsJobRemovable(): void
    {
        $site = $this->site();
        $backup = $this->job($site, 'backup');
        $this->agent->scriptJob('site.cron', ['running', 'failed'], errorCode: 'internal', message: 'cron.d write failed');

        $this->tab($site)->callTableAction('delete', $backup, ['current_password' => self::PASSWORD]);
        $this->trackToTheEnd(AgentJob::sole());

        $this->assertSame(SiteCronJob::FAILED, $backup->refresh()->status);
        $this->assertSame('cron.d write failed', $site->refresh()->last_error);
        $this->tab($site)->assertSee(__('zimam.schedule.status.failed'))->assertTableActionEnabled('delete', $backup);

        $this->agent->scriptJob('site.cron', ['running', 'succeeded']);
        $this->tab($site)->callTableAction('delete', $backup, ['current_password' => self::PASSWORD])->assertHasNoTableActionErrors();
        $this->trackToTheEnd(AgentJob::query()->latest('id')->first());

        $this->assertSame(0, $site->cronJobs()->count());
        $this->assertNull($site->refresh()->last_error);
    }

    public function testJobFieldsValidated(): void
    {
        $site = $this->site();
        $this->job($site, 'report');

        $cases = [
            ['schedule' => '@reboot'],
            ['schedule' => '*/5 * * *'],
            ['schedule' => '0 3 * * 1%2'],
            ['command' => ''],
            ['command' => "php artisan a\nrm -rf ~"],
            ['command' => "a\rb"],
            ['command' => "a\0b"],
            ['command' => str_repeat('é', 2049)],
            ['job_id' => 'Report'],
            ['job_id' => 'scheduler'],
            ['job_id' => 'report'], // taken
            ['job_id' => ''],
        ];
        foreach ($cases as $over) {
            $this->tab($site)->callAction('add', self::form($over))->assertHasActionErrors([array_key_first($over)]);
        }
        $this->assertSame([], $this->submitted('site.cron'));
    }

    public function testTooManyJobsRefusesAdd(): void
    {
        $site = $this->site();
        foreach (range(1, SiteCronJob::MAX_JOBS) as $i) {
            $this->job($site, "job{$i}");
        }

        $this->tab($site)->assertActionDisabled('add');
    }

    public function testNextRunsShown(): void
    {
        $this->travelTo('2027-03-26 12:00:00');
        $site = $this->site();
        $this->agent->changeServer(fn (array $s): array => [...$s, 'timezone' => 'Europe/Berlin']);
        $this->job($site, 'report', ['schedule' => '0 3 * * *']);
        $this->job($site, 'paused', ['schedule' => '*/5 * * * *', 'enabled' => false]);

        $html = $this->tab($site)
            ->assertSee('Europe/Berlin')
            ->assertSee(__('zimam.schedule.paused'))
            ->html();

        // Five runs, 03:00 local on both sides of the change to summer time.
        foreach (['Sat 27 Mar 2027, 03:00', 'Sun 28 Mar 2027, 03:00', 'Mon 29 Mar 2027, 03:00', 'Tue 30 Mar 2027, 03:00', 'Wed 31 Mar 2027, 03:00'] as $time) {
            $this->assertStringContainsString($time, $html);
        }
        $this->assertStringNotContainsString('Thu 1 Apr 2027, 03:00', $html);
    }

    public function testZoneFallsBackToUtc(): void
    {
        $site = $this->site();
        $this->agent->changeServer(fn (array $s): array => array_diff_key($s, ['timezone' => true]));
        $this->job($site, 'report');

        $this->tab($site)->assertSee('UTC');
    }

    public function testCronLogListed(): void
    {
        $site = $this->site();
        $this->agent->setLog('site.cron', ['2027-01-01T00:00:00+00:00 [report] started'], 'shop');

        Livewire::test(SiteLogs::class, ['site' => $site])
            ->assertSeeHtml('value="site.cron"')
            ->assertSee(__('zimam.logs.sources.site.cron'));
        Livewire::test(LogViewer::class, ['source' => 'site.cron', 'site' => 'shop'])->assertSee('[report] started');

        $reads = array_values(array_filter($this->submitted('logs.read'), fn (array $p): bool => $p['source'] === 'site.cron'));
        $this->assertSame(['source' => 'site.cron', 'name' => 'shop', 'direction' => 'back', 'limit' => 200], $reads[0]);
        $this->assertContains('site.cron', array_column($this->submitted('logs.list'), 'source'), 'a file source lists its archives');

        // A static site may have jobs, so it has the log too.
        $static = Site::create(['name' => 'docs', 'type' => Site::STATIC, 'status' => Site::ACTIVE]);
        Livewire::test(SiteLogs::class, ['site' => $static])->assertSeeHtml('value="site.cron"');
    }

    public function testAgentActionsWaitForSiteJob(): void
    {
        $site = $this->site();
        $report = $this->job($site, 'report');
        $site->jobs()->create(['command' => 'site.deploy', 'status' => AgentJob::RUNNING, 'agent_job_id' => '01JBX7Q3A1B2C3D4E5F6G7H8J9', 'params' => []]);

        $this->tab($site)->assertActionDisabled('add')->assertActionDisabled('scheduler')->assertTableActionDisabled('edit', $report);
    }

    public function testScheduleTabOnSitePage(): void
    {
        $site = $this->site();
        $this->job($site, 'report');

        Livewire::test(ViewSite::class, ['record' => $site->getRouteKey()])->assertSee(__('zimam.sites.tabs.schedule'));
        $this->tab($site)->assertSee(__('zimam.schedule.heading'))->assertSee('php artisan report');

        $static = Site::create(['name' => 'docs', 'type' => Site::STATIC, 'status' => Site::ACTIVE]);
        $static->domains()->create(['hostname' => 'docs.example.com', 'is_primary' => true]);
        Livewire::test(ViewSite::class, ['record' => $static->getRouteKey()])->assertSee(__('zimam.sites.tabs.schedule'));
    }

    public function testCronParamsAreWhatTheCommandSends(): void
    {
        $command = new SiteCron('shop', true, [['id' => 'report', 'schedule' => '@daily', 'command' => 'x', 'enabled' => true]]);

        $this->assertSame('site.cron', $command->name());
        $this->assertSame(120, $command->runtimeMaxSeconds());
        $this->assertSame(['name' => 'shop', 'scheduler' => true, 'jobs' => [['id' => 'report', 'schedule' => '@daily', 'command' => 'x', 'enabled' => true]]], $command->params());
    }
}
