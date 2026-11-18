<?php

namespace Tests\Feature\Workers;

use App\Agent\Commands\SystemMetrics;
use App\Filament\Resources\SiteResource\Pages\ViewSite;
use App\Filament\Resources\SiteResource\Pages\WorkersTab;
use App\Livewire\LogViewer;
use App\Livewire\SiteLogs;
use App\Models\AgentJob;
use App\Models\MetricSample;
use App\Models\Site;
use App\Models\SiteWorker;
use App\Support\MetricWarnings;
use Carbon\CarbonImmutable;
use Livewire\Features\SupportTesting\Testable;
use Livewire\Livewire;
use Tests\Feature\Databases\DatabasesTestCase;
use Tests\Support\MetricSamples;

/**
 * A site's Workers tab (v0.4.0): its queue workers, saved as one whole list
 * through site.workers, each process's state from site.workers.status,
 * restarts, the workers' journal, and the crash-loop and memory warnings
 * (R04-D17, R04-D18).
 */
final class WorkersTabTest extends DatabasesTestCase
{
    use MetricSamples;

    private function tab(Site $site): Testable
    {
        return Livewire::test(WorkersTab::class, ['site' => $site]);
    }

    /** A stored worker, as a saved list leaves it. */
    private function worker(Site $site, string $id, array $attributes = []): SiteWorker
    {
        return $site->workers()->create(array_replace([
            'worker_id' => $id,
            'kind' => SiteWorker::LARAVEL_QUEUE,
            'connection' => 'default',
            'queues' => [],
            'processes' => 1,
            'memory_mb' => 512,
            'status' => SiteWorker::ACTIVE,
        ], $attributes));
    }

    /** @return array<string, mixed> the Add form for a Laravel queue worker */
    private static function queueForm(array $over = []): array
    {
        return array_replace([
            'worker_id' => 'queue',
            'kind' => SiteWorker::LARAVEL_QUEUE,
            'processes' => 2,
            'memory_mb' => 256,
            'connection' => 'redis',
            'queues' => 'high, default',
            'tries' => 3,
            'timeout' => 60,
            'sleep' => null,
            'max_time' => 3600,
            'backoff' => null,
        ], $over);
    }

    public function testAddWorkerSubmitsWholeList(): void
    {
        $site = $this->site();
        $this->worker($site, 'mail', ['kind' => SiteWorker::CUSTOM, 'connection' => null, 'command' => 'php artisan horizon', 'processes' => 1, 'memory_mb' => 1024]);

        $this->tab($site)
            ->callAction('add', self::queueForm())
            ->assertHasNoActionErrors()
            ->assertNotified(__('zimam.workers.submitted', ['name' => 'shop']));

        $this->assertSame([[
            'name' => 'shop',
            'workers' => [
                ['id' => 'mail', 'kind' => 'custom', 'processes' => 1, 'memory_mb' => 1024, 'command' => 'php artisan horizon'],
                ['id' => 'queue', 'kind' => 'laravel-queue', 'processes' => 2, 'memory_mb' => 256, 'connection' => 'redis', 'queues' => ['high', 'default'], 'tries' => 3, 'timeout' => 60, 'max_time' => 3600],
            ],
        ]], $this->submitted('site.workers'));
        $this->assertSame(['mail', 'queue'], $site->workers()->orderBy('worker_id')->pluck('worker_id')->all());
        $this->assertSame([SiteWorker::APPLYING], $site->workers()->distinct()->pluck('status')->all());

        $this->trackToTheEnd(AgentJob::sole());

        $this->assertSame([SiteWorker::ACTIVE], $site->workers()->distinct()->pluck('status')->all());
    }

    public function testEditAndDeleteSubmitWholeList(): void
    {
        $site = $this->site();
        $mail = $this->worker($site, 'mail');
        $queue = $this->worker($site, 'queue');

        $this->tab($site)->callTableAction('edit', $queue, self::queueForm(['processes' => 4, 'queues' => '']))->assertHasNoTableActionErrors();
        $this->trackToTheEnd(AgentJob::sole());
        $this->assertSame(4, $queue->refresh()->processes);

        $this->tab($site)->callTableAction('delete', $mail, ['current_password' => 'not the password'])->assertHasTableActionErrors(['current_password']);
        $this->tab($site)->callTableAction('delete', $mail, ['current_password' => self::PASSWORD])->assertHasNoTableActionErrors();

        $lists = array_column($this->submitted('site.workers'), 'workers');
        $this->assertSame(['mail', 'queue'], array_column($lists[0], 'id'));
        $this->assertSame(4, $lists[0][1]['processes']);
        $this->assertArrayNotHasKey('queues', $lists[0][1]);
        $this->assertSame(['queue'], array_column($lists[1], 'id'));
        $this->assertSame(['queue'], $site->workers()->pluck('worker_id')->all());
    }

    public function testCustomCommandRefusesNewline(): void
    {
        $site = $this->site();

        foreach (["php artisan horizon\nrm -rf ~", "php artisan horizon\r", "a\0b", '', str_repeat('é', 2049)] as $command) {
            $this->tab($site)
                ->callAction('add', ['worker_id' => 'horizon', 'kind' => SiteWorker::CUSTOM, 'processes' => 1, 'memory_mb' => 512, 'command' => $command])
                ->assertHasActionErrors(['command']);
        }

        $this->assertSame([], $this->submitted('site.workers'));
        $this->assertSame(0, SiteWorker::count());
    }

    public function testQueueNamesValidated(): void
    {
        $site = $this->site();
        $this->worker($site, 'queue');

        $cases = [
            ['queues' => 'high, bad queue'],
            ['queues' => implode(',', array_map(fn (int $i): string => "q{$i}", range(1, 11)))],
            ['queues' => str_repeat('q', 65)],
            ['connection' => 'Redis'],
            ['worker_id' => 'Queue'],
            ['worker_id' => 'queue'], // taken
            ['max_time' => 30],
            ['tries' => 0],
            ['processes' => 11],
            ['memory_mb' => 63],
        ];
        foreach ($cases as $over) {
            $field = array_key_first($over);
            $this->tab($site)->callAction('add', self::queueForm($over))->assertHasActionErrors([$field]);
        }
        $this->assertSame([], $this->submitted('site.workers'));

        $this->tab($site)->callAction('add', self::queueForm(['worker_id' => 'q2', 'queues' => 'emails:high,App.Jobs_1-x', 'max_time' => 0]))->assertHasNoActionErrors();
        $this->assertSame(['emails:high', 'App.Jobs_1-x'], $this->submitted('site.workers')[0]['workers'][1]['queues']);
        $this->assertSame(0, $this->submitted('site.workers')[0]['workers'][1]['max_time']);
    }

    public function testStatusShowsEachProcess(): void
    {
        $site = $this->site();
        $this->worker($site, 'queue', ['processes' => 2]);
        $this->agent->respond('site.workers.status', ['name' => 'shop', 'workers' => [
            ['id' => 'queue', 'instances' => [
                ['instance' => 1, 'active' => 'active', 'sub' => 'running', 'restarts' => 0, 'since' => '2026-11-18T10:00:00Z', 'failing' => false],
                ['instance' => 2, 'active' => 'activating', 'sub' => 'auto-restart', 'restarts' => 7, 'since' => '2026-11-18T10:05:00Z', 'failing' => true],
            ]],
        ]]);

        $this->tab($site)
            ->assertSee(__('zimam.workers.process.running', ['n' => 1]))
            ->assertSee(__('zimam.workers.process.failing', ['n' => 2, 'restarts' => 7]));
        $this->assertSame([['name' => 'shop']], $this->submitted('site.workers.status'));
    }

    public function testRestartOne(): void
    {
        $site = $this->site();
        $queue = $this->worker($site, 'queue');
        $this->agent->workers('shop', [$queue->spec()]);

        $this->tab($site)->callTableAction('restart', $queue)->assertNotified(__('zimam.workers.restart.submitted', ['worker' => 'queue']));
        $this->trackToTheEnd(AgentJob::sole());

        $this->tab($site)->callAction('restartAll')->assertNotified(__('zimam.workers.restart.submitted_all'));

        $this->assertSame([['name' => 'shop', 'worker' => 'queue'], ['name' => 'shop']], $this->submitted('site.workers.restart'));
    }

    public function testAgentActionsWaitForSiteJob(): void
    {
        $site = $this->site();
        $queue = $this->worker($site, 'queue');
        $site->jobs()->create(['command' => 'site.deploy', 'status' => AgentJob::RUNNING, 'agent_job_id' => '01JBX7Q3A1B2C3D4E5F6G7H8J9', 'params' => []]);

        $this->tab($site)
            ->assertActionDisabled('add')
            ->assertActionDisabled('restartAll')
            ->assertTableActionDisabled('restart', $queue);
    }

    public function testWorkersJournalListed(): void
    {
        $site = $this->site();
        $this->agent->setLog('site.workers', ['Processing: App\\Jobs\\SendMail'], 'shop');

        Livewire::test(SiteLogs::class, ['site' => $site])
            ->assertSeeHtml('value="site.workers"')
            ->assertSee(__('zimam.logs.sources.site.workers'));
        Livewire::test(LogViewer::class, ['source' => 'site.workers', 'site' => 'shop'])
            ->assertSee('Processing: App\\Jobs\\SendMail')
            ->set('priority', 3);

        $reads = array_values(array_filter($this->submitted('logs.read'), fn (array $p): bool => $p['source'] === 'site.workers'));
        $this->assertSame(['source' => 'site.workers', 'name' => 'shop', 'direction' => 'back', 'limit' => 200], $reads[0]);
        $this->assertSame(3, $reads[1]['priority'], 'a journal source takes a priority');
        $this->assertNotContains('site.workers', array_column($this->submitted('logs.list'), 'source'), 'a journal has no archives to list');

        $static = Site::create(['name' => 'docs', 'type' => Site::STATIC, 'status' => Site::ACTIVE]);
        Livewire::test(SiteLogs::class, ['site' => $static])->assertDontSeeHtml('value="site.workers"');
    }

    public function testRestartingWorkerWarning(): void
    {
        $at = CarbonImmutable::now();
        $metrics = $this->agent->call(new SystemMetrics)->data;
        $worker = fn (int $restarts, bool $failing = true, bool $failed = false): array => ['site' => 'shop', 'worker' => 'queue', 'restarts' => $restarts, 'failing' => $failing, 'failed' => $failed];
        $at6 = $this->sample($at->subMinutes(6));
        $at6->update(['workers' => [$worker(10)]]);
        $this->sample($at->subMinutes(15))->update(['workers' => [$worker(0)]]);

        // 10 to 15 in the window: 5 isn't more than 5.
        $this->assertSame([], MetricWarnings::from([...$metrics, 'workers' => [$worker(15)]], $at));
        // 10 to 16 while failing warns, keyed per worker.
        $warnings = MetricWarnings::from([...$metrics, 'workers' => [$worker(16)]], $at);
        $this->assertSame(['worker-crashloop:shop:queue'], array_column($warnings, 'key'));
        $this->assertSame('warning', $warnings[0]['severity']);
        // Clean exits (max_time, queue:restart) count too, so not while it isn't failing.
        $this->assertSame([], MetricWarnings::from([...$metrics, 'workers' => [$worker(40, failing: false)]], $at));
        // A drop is a reset (reset-failed), not negative growth: 10, then 0, then 7.
        $this->sample($at->subMinutes(3))->update(['workers' => [$worker(0)]]);
        $this->assertSame(['worker-crashloop:shop:queue'], array_column(MetricWarnings::from([...$metrics, 'workers' => [$worker(7)]], $at), 'key'));
        // systemd gave up on it: critical, whatever the count.
        $failed = MetricWarnings::from([...$metrics, 'workers' => [$worker(0, failing: true, failed: true)]], $at);
        $this->assertSame(['worker-crashloop:shop:queue'], array_column($failed, 'key'));
        $this->assertSame('critical', $failed[0]['severity']);

        // The sampler keeps the workers for the next comparison.
        $this->agent->changeMetrics(fn (array $m): array => [...$m, 'workers' => [$worker(3)]]);
        $this->artisan('zimam:sample-metrics')->assertSuccessful();
        $this->assertSame([$worker(3)], MetricSample::query()->latest('id')->first()->workers);
    }

    public function testMemoryOverServerRamWarnsButSaves(): void
    {
        $site = $this->site();
        // The fake server has 4 GiB; 3 × 1024 + 2 × 600 MB is over it.
        $this->worker($site, 'mail', ['processes' => 3, 'memory_mb' => 1024]);

        $this->tab($site)
            ->callAction('add', self::queueForm(['processes' => 2, 'memory_mb' => 600]))
            ->assertHasNoActionErrors()
            ->assertNotified(__('zimam.workers.memory_over', ['total' => '4.2 GiB', 'ram' => '4 GiB']))
            ->assertSee(__('zimam.workers.memory_over', ['total' => '4.2 GiB', 'ram' => '4 GiB']));

        $this->assertCount(1, $this->submitted('site.workers'));
        $this->assertSame(2, $site->workers()->count());
    }

    public function testFailedSaveMarksWorkersFailed(): void
    {
        $site = $this->site();
        $this->agent->scriptJob('site.workers', ['running', 'failed'], errorCode: 'internal', message: 'systemd-analyze verify failed');

        $this->tab($site)->callAction('add', self::queueForm());
        $this->trackToTheEnd(AgentJob::sole());

        $this->assertSame(SiteWorker::FAILED, $site->workers()->sole()->status);
        $this->assertSame('systemd-analyze verify failed', $site->refresh()->last_error);
    }

    /** The tab is on a PHP or Laravel site's page, and renders with its notes (OQ2). */
    public function testWorkersTabOnSitePage(): void
    {
        $site = $this->site();
        $this->worker($site, 'queue');

        Livewire::test(ViewSite::class, ['record' => $site->getRouteKey()])->assertSee(__('zimam.sites.tabs.workers'));
        $this->tab($site)
            ->assertSee(__('zimam.workers.heading'))
            ->assertSee(__('zimam.workers.help.reverb'))
            ->assertSee(__('zimam.workers.help.horizon', ['prefix' => 's_shop:horizon:']))
            ->assertSee(__('zimam.workers.summary.laravel-queue', ['connection' => 'default', 'queues' => __('zimam.workers.summary.default_queue')]));

        $static = Site::create(['name' => 'docs', 'type' => Site::STATIC, 'status' => Site::ACTIVE]);
        $static->domains()->create(['hostname' => 'docs.example.com', 'is_primary' => true]);
        Livewire::test(ViewSite::class, ['record' => $static->getRouteKey()])->assertDontSee(__('zimam.sites.tabs.workers'));
    }

    public function testStaticSiteHasNoWorkers(): void
    {
        $static = Site::create(['name' => 'docs', 'type' => Site::STATIC, 'status' => Site::ACTIVE]);

        $this->tab($static)->assertSee(__('zimam.workers.static'))->assertActionHidden('add');
        $this->assertSame([], $this->submitted('site.workers.status'));
    }
}
