<?php

namespace Tests\Feature\Jobs;

use App\Agent\AgentClient;
use App\Agent\Commands\E2eSleep;
use App\Agent\Exceptions\AgentConflict;
use App\Agent\Exceptions\AgentUnavailable;
use App\Agent\FakeAgentClient;
use App\Filament\Resources\AgentJobResource\Pages\ListAgentJobs;
use App\Filament\Resources\AgentJobResource\Pages\ViewAgentJob;
use App\Filament\Resources\SiteResource\Pages\ViewSite;
use App\Livewire\SiteJobs;
use App\Models\AgentJob;
use App\Models\Site;
use App\Models\User;
use Illuminate\Foundation\Testing\RefreshDatabase;
use Illuminate\Support\Facades\Queue;
use Illuminate\Support\Str;
use Livewire\Livewire;
use Tests\TestCase;

/** The Jobs screen (§7.3): every job, filters, a job's page and Cancel. */
final class JobsScreenTest extends TestCase
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

    private function job(string $command, string $status, ?string $agentJobId = null, ?Site $related = null): AgentJob
    {
        $job = new AgentJob(['agent_job_id' => $agentJobId, 'command' => $command, 'subject' => 'server', 'status' => $status, 'params' => ['version' => '8.4']]);
        $job->related()->associate($related);
        $job->save();

        return $job;
    }

    /** A job the fake agent really runs, so cancel has something to stop. */
    private function liveJob(): AgentJob
    {
        $accepted = $this->agent->submit(new E2eSleep(30, 'jobs'));

        return $this->job('e2e.sleep', AgentJob::RUNNING, $accepted->id);
    }

    private static function ulid(): string
    {
        return strtoupper((string) Str::ulid());
    }

    public function testListShowsJobs(): void
    {
        $jobs = [$this->job('php.install', AgentJob::SUCCEEDED), $this->job('site.provision', AgentJob::FAILED)];

        Livewire::test(ListAgentJobs::class)
            ->assertCanSeeTableRecords($jobs)
            ->assertSee(['php.install', 'site.provision', __('zimam.jobs.status.succeeded'), __('zimam.jobs.status.failed')]);
    }

    public function testFilters(): void
    {
        $install = $this->job('php.install', AgentJob::SUCCEEDED);
        $failed = $this->job('site.provision', AgentJob::FAILED);
        $running = $this->job('site.provision', AgentJob::RUNNING);

        Livewire::test(ListAgentJobs::class)
            ->filterTable('status', AgentJob::FAILED)
            ->assertCanSeeTableRecords([$failed])
            ->assertCanNotSeeTableRecords([$install, $running]);
        Livewire::test(ListAgentJobs::class)
            ->filterTable('command', 'site.provision')
            ->assertCanSeeTableRecords([$failed, $running])
            ->assertCanNotSeeTableRecords([$install]);
    }

    public function testViewShowsStatusTimesAndError(): void
    {
        $job = $this->job('site.provision', AgentJob::FAILED);
        $job->update(['error_message' => 'configuration test failed: nginx: [emerg] bogus', 'started_at' => now(), 'finished_at' => now()]);

        Livewire::test(ViewAgentJob::class, ['record' => $job->getRouteKey()])
            ->assertOk()
            ->assertSee(['site.provision', __('zimam.jobs.status.failed'), 'configuration test failed: nginx: [emerg] bogus']);
    }

    public function testCancelSubmits(): void
    {
        $job = $this->liveJob();

        Livewire::test(ViewAgentJob::class, ['record' => $job->getRouteKey()])
            ->assertActionVisible('cancel')
            ->callAction('cancel')
            ->assertNotified(__('zimam.jobs.cancel.done'));

        $cancels = array_values(array_filter($this->agent->calls(), fn (array $c): bool => $c['command'] === 'job.cancel'));
        $this->assertSame([['job' => $job->agent_job_id]], array_column($cancels, 'params'));
    }

    /** The agent's refusal, such as dpkg running, reaches the user as it is. */
    public function testCancelRefusalShown(): void
    {
        $job = $this->liveJob();
        $message = "Job {$job->agent_job_id} is installing or removing packages.";
        $this->agent->respond('job.cancel', new AgentConflict($message));

        Livewire::test(ViewAgentJob::class, ['record' => $job->getRouteKey()])
            ->callAction('cancel')
            ->assertNotified($message);
    }

    public function testCancelHiddenWhenFinished(): void
    {
        foreach ([AgentJob::SUCCEEDED, AgentJob::FAILED, AgentJob::CANCELLED, AgentJob::WAITING] as $status) {
            $job = $this->job('php.install', $status, $status === AgentJob::WAITING ? null : self::ulid());
            Livewire::test(ViewAgentJob::class, ['record' => $job->getRouteKey()])->assertActionHidden('cancel');
        }
        $queued = $this->job('php.install', AgentJob::QUEUED, self::ulid());
        Livewire::test(ViewAgentJob::class, ['record' => $queued->getRouteKey()])->assertActionVisible('cancel');
    }

    public function testCancelPausedWhileAgentUnreachable(): void
    {
        $job = $this->liveJob();
        $this->agent->respond('agent.version', new AgentUnavailable('connect: no such file'));

        Livewire::test(ViewAgentJob::class, ['record' => $job->getRouteKey()])->assertActionDisabled('cancel');
    }

    public function testSiteJobsTab(): void
    {
        $site = Site::create(['name' => 'shop', 'type' => 'static', 'status' => Site::ACTIVE]);
        $site->domains()->create(['hostname' => 'shop.example.com', 'is_primary' => true]);
        $other = Site::create(['name' => 'blog', 'type' => 'static', 'status' => Site::ACTIVE]);
        $mine = $this->job('site.provision', AgentJob::SUCCEEDED, null, $site);
        $theirs = $this->job('site.provision', AgentJob::SUCCEEDED, null, $other);
        $server = $this->job('php.install', AgentJob::SUCCEEDED);

        Livewire::test(SiteJobs::class, ['site' => $site])
            ->assertCanSeeTableRecords([$mine])
            ->assertCanNotSeeTableRecords([$theirs, $server]);
        Livewire::test(ViewSite::class, ['record' => $site->getRouteKey()])
            ->assertSeeLivewire(SiteJobs::class);
    }
}
