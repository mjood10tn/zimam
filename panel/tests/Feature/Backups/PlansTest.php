<?php

namespace Tests\Feature\Backups;

use App\Agent\Exceptions\AgentBusy;
use App\Agent\Exceptions\AgentValidationFailed;
use App\Filament\Pages\Backups\PlansTable;
use App\Models\AgentJob;
use App\Models\BackupPlan;
use App\Models\Site;
use Livewire\Livewire;

/** Backup plans: create, edit, run now, delete; sites and databases that go leave them (R05-D13). */
final class PlansTest extends BackupsTestCase
{
    public function testNewPlanLeavesPanelOff(): void
    {
        $this->destination();

        Livewire::test(PlansTable::class)
            ->mountAction('create')
            ->assertActionDataSet(['include_panel' => false, 'frequency' => 'daily', 'keep_last' => BackupPlan::DEFAULT_KEEP_LAST]);
    }

    public function testCreatePlanSubmits(): void
    {
        $destination = $this->destination();
        $shop = $this->site();
        $db = $this->database($shop);

        Livewire::test(PlansTable::class)
            ->callAction('create', [
                'key' => 'nightly',
                'destination' => $destination->id,
                'sites' => [$shop->id],
                'databases' => [$db->id],
                'include_panel' => false,
                'frequency' => 'weekly',
                'day' => 'sun',
                'time' => '03:15',
                'keep_last' => 5,
                'keep_daily' => 7,
                'keep_weekly' => 0,
                'keep_monthly' => 3,
            ])
            ->assertHasNoActionErrors()
            ->assertNotified(__('zimam.backups.plans.save.submitted', ['key' => 'nightly']));

        $this->assertSame([[
            'id' => 'nightly',
            'destination' => 'offsite',
            'sites' => ['shop'],
            'databases' => ['s_shop_main'],
            'schedule' => 'weekly sun 03:15',
            'retention' => ['keep_last' => 5, 'keep_daily' => 7, 'keep_monthly' => 3],
        ]], $this->submitted('backup.plan'));
        $plan = BackupPlan::sole();
        $this->assertSame(BackupPlan::SAVING, $plan->status);
        $this->assertSame([$shop->id], $plan->sites->pluck('id')->all());
        $this->trackToTheEnd(AgentJob::sole());
        $this->assertSame(BackupPlan::ACTIVE, $plan->refresh()->status);
    }

    public function testPlanNeedsSomethingToBackUp(): void
    {
        $destination = $this->destination();

        Livewire::test(PlansTable::class)
            ->callAction('create', ['key' => 'empty', 'destination' => $destination->id, 'frequency' => 'hourly', 'keep_last' => 3])
            ->assertHasActionErrors(['sites']);
        $this->assertSame([], $this->submitted('backup.plan'));
    }

    public function testRefusedPlanIsNotKept(): void
    {
        $destination = $this->destination();
        $this->agent->respond('backup.plan', new AgentValidationFailed('Invalid parameters', ['schedule' => 'is wrong']));

        Livewire::test(PlansTable::class)
            ->callAction('create', ['key' => 'p1', 'destination' => $destination->id, 'include_panel' => true, 'frequency' => 'hourly', 'keep_last' => 3])
            ->assertNotified('is wrong');
        $this->assertSame(0, BackupPlan::count());
    }

    public function testEditSubmitsTheNewSettings(): void
    {
        $plan = $this->plan($this->destination(), sites: [$this->site()]);
        $blog = $this->site('blog');

        Livewire::test(PlansTable::class)
            ->callTableAction('edit', $plan, ['sites' => [$blog->id], 'frequency' => 'hourly', 'keep_last' => 2])
            ->assertHasNoTableActionErrors();

        $this->assertSame(['blog'], $this->submitted('backup.plan')[0]['sites']);
        $this->assertSame('hourly', $this->submitted('backup.plan')[0]['schedule']);
        $this->assertSame([$blog->id], $plan->sites()->pluck('sites.id')->all());
    }

    public function testRunNowSubmits(): void
    {
        $plan = $this->plan($this->destination(), sites: [$this->site()]);

        Livewire::test(PlansTable::class)
            ->callTableAction('run', $plan)
            ->assertNotified(__('zimam.backups.plans.run.submitted', ['key' => 'nightly']));

        $this->assertSame([['plan' => 'nightly']], $this->submitted('backup.run'));
        $job = AgentJob::sole();
        $this->assertSame(21600, $job->runtime_max_seconds);
        $this->trackToTheEnd($job);
        $this->assertSame('backup:nightly', $job->subject);
        // The outcome reads the run's record.
        $this->assertSame(1, $plan->runs()->where('status', 'succeeded')->where('trigger', 'panel')->count());
    }

    public function testRunNowWhileItRunsSaysSo(): void
    {
        $plan = $this->plan($this->destination(), sites: [$this->site()]);
        $this->agent->respond('backup.run', new AgentBusy('backup:nightly already has a job running'));

        Livewire::test(PlansTable::class)
            ->callTableAction('run', $plan)
            ->assertNotified(__('zimam.backups.plans.run.busy'));
        $this->assertSame(0, AgentJob::count());
    }

    public function testDeleteNeedsPasswordAndRemovesThePlan(): void
    {
        $plan = $this->plan($this->destination(), sites: [$this->site()]);

        Livewire::test(PlansTable::class)
            ->callTableAction('delete', $plan, ['current_password' => self::PASSWORD])
            ->assertHasNoTableActionErrors();
        $this->assertSame([['plan' => 'nightly']], $this->submitted('backup.plan.delete'));
        $this->trackToTheEnd(AgentJob::sole());
        $this->assertModelMissing($plan);
    }

    /** The agent forgets a deleted site and a dropped database in every plan; the pivots follow. */
    public function testDeletedSiteAndDroppedDatabaseLeaveThePlans(): void
    {
        $shop = $this->site();
        $blog = $this->site('blog');
        $db = $this->database($blog);
        $plan = $this->plan($this->destination(), sites: [$shop, $blog], databases: [$db]);

        $db->delete();
        $shop->delete();

        $this->assertSame(['blog'], $plan->sites()->pluck('name')->all());
        $this->assertSame([], $plan->databases()->pluck('name')->all());
        $this->assertSame(1, Site::count());
    }
}
