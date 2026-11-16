<?php

namespace Tests\Feature\Deployments;

use App\Actions\StartDeployment;
use App\Agent\AgentJobStatus;
use App\Agent\Commands\SiteDeploy;
use App\Agent\Exceptions\AgentBusy;
use App\Agent\Exceptions\AgentConflict;
use App\Agent\JobOutcomes;
use App\Filament\Resources\AgentJobResource\Pages\ViewAgentJob;
use App\Jobs\SubmitAgentJob;
use App\Models\AgentJob;
use App\Models\Deployment;
use App\Models\Site;
use App\Models\SiteRepository;
use Livewire\Livewire;

/**
 * Deploys that wait after `busy` (R02-D12, review run 3): at most one waits
 * per site, a waiting one can be cancelled, and a deploy that failed after
 * its release went live still names the release.
 */
final class WaitingDeployTest extends DeploymentsTestCase
{
    private const OLDER = 'c0ffee0000000000000000000000000000000005';

    private const NEWER = 'c0ffee0000000000000000000000000000000006';

    private function retry(AgentJob $job, Site $site, ?string $commit = null): void
    {
        $command = new SiteDeploy(site: $site->name, trigger: Deployment::WEBHOOK, composer: false, keepReleases: 5, script: '', commit: $commit);
        $retry = (new SubmitAgentJob($job, $command))->withFakeQueueInteractions();
        app()->call([$retry, 'handle']);
    }

    /** The newer push takes the older one's place, so the two can't start in the wrong order. */
    public function testNewerDeploySupersedesTheWaitingOne(): void
    {
        $site = $this->site();
        $this->connected($site);
        $this->agent->respond('site.deploy', new AgentBusy('site:shop already has a job running'));

        $older = app(StartDeployment::class)->run($site, Deployment::WEBHOOK, self::OLDER);
        $newer = app(StartDeployment::class)->run($site, Deployment::WEBHOOK, self::NEWER);

        $older->refresh();
        $this->assertSame(Deployment::CANCELLED, $older->status);
        $this->assertSame(__('zimam.deployments.deploy.superseded'), $older->error_message);
        $this->assertSame('superseded', $older->agentJob->error_code);
        $this->assertNotNull($older->agentJob->finished_at);
        $this->assertSame(Deployment::WAITING, $newer->status);
        $this->assertSame(1, AgentJob::where('status', AgentJob::WAITING)->count());

        // The older one's queued retry finds it finished and submits nothing.
        $submits = count($this->submitted('site.deploy'));
        $this->retry($older->agentJob, $site, self::OLDER);
        $this->assertCount($submits, $this->submitted('site.deploy'));
        $this->assertSame(Deployment::CANCELLED, $older->fresh()->status);
    }

    /** Deploy now and the deploy URL deploy the head: one that already waits is the same deploy. */
    public function testHeadDeployReusesTheWaitingOne(): void
    {
        $site = $this->site();
        $this->connected($site);
        $this->agent->respond('site.deploy', new AgentBusy('site:shop already has a job running'));

        $first = app(StartDeployment::class)->run($site, Deployment::URL);
        $submits = count($this->submitted('site.deploy'));
        $second = app(StartDeployment::class)->run($site, Deployment::URL);
        $third = app(StartDeployment::class)->run($site, Deployment::MANUAL, by: $this->user);

        $this->assertSame($first->id, $second->id);
        $this->assertSame($first->id, $third->id);
        $this->assertSame(1, Deployment::count());
        $this->assertSame(1, AgentJob::count());
        $this->assertCount($submits, $this->submitted('site.deploy'));

        // A push names its commit, so it replaces the waiting head deploy.
        $push = app(StartDeployment::class)->run($site, Deployment::WEBHOOK, self::NEWER);
        $this->assertSame(Deployment::CANCELLED, $first->fresh()->status);
        $this->assertSame(Deployment::WAITING, $push->status);
    }

    /** A deploy the agent refuses replaces nothing. */
    public function testRefusedDeployLeavesTheWaitingOne(): void
    {
        $site = $this->site();
        $this->connected($site);
        $this->agent->respond('site.deploy', new AgentBusy('site:shop already has a job running'), new AgentConflict('Site shop has no repository to deploy from; connect one first.'));

        $waiting = app(StartDeployment::class)->run($site, Deployment::WEBHOOK, self::OLDER);
        try {
            app(StartDeployment::class)->run($site, Deployment::WEBHOOK, self::NEWER);
            $this->fail('the refusal was not rethrown');
        } catch (AgentConflict) {
        }

        $this->assertSame(Deployment::WAITING, $waiting->fresh()->status);
    }

    /** Another site's waiting deploy, and this site's other waiting jobs, are left alone. */
    public function testOnlyTheSitesWaitingDeploysAreSuperseded(): void
    {
        $shop = $this->site();
        $blog = $this->site(name: 'blog');
        $this->connected($shop);
        $this->agent->connectRepository('blog', 'octo/blog', 'main');
        SiteRepository::createFor($blog, 'octo/blog', 'main')->update(['deploy_key_fingerprint' => 'SHA256:+DiY3wvvV6TuJJhbpZisF/zLDA0zPMSvHdkr4UvCOqU', 'deploy_key_public' => 'ssh-ed25519 AAAA']);
        $this->agent->respond('site.deploy', new AgentBusy('the server already runs two jobs'));
        $other = new AgentJob(['command' => 'site.env.write', 'status' => AgentJob::WAITING, 'params' => []]);
        $other->related()->associate($shop);
        $other->save();

        $blogs = app(StartDeployment::class)->run($blog->fresh(), Deployment::WEBHOOK, self::OLDER);
        app(StartDeployment::class)->run($shop, Deployment::WEBHOOK, self::NEWER);

        $this->assertSame(Deployment::WAITING, $blogs->fresh()->status);
        $this->assertSame(AgentJob::WAITING, $other->fresh()->status);
    }

    /** The Cancel of a waiting deploy ends its row too, with the reason. */
    public function testCancellingAWaitingDeploy(): void
    {
        $site = $this->site();
        $this->connected($site);
        $this->agent->respond('site.deploy', new AgentBusy('site:shop already has a job running'));
        $deployment = app(StartDeployment::class)->run($site, Deployment::WEBHOOK, self::OLDER);

        Livewire::test(ViewAgentJob::class, ['record' => $deployment->agentJob->getRouteKey()])
            ->callAction('cancelWaiting')
            ->assertNotified(__('zimam.jobs.cancel.done'));

        $deployment->refresh();
        $this->assertSame(Deployment::CANCELLED, $deployment->status);
        $this->assertSame(__('zimam.jobs.cancel.waiting_reason'), $deployment->error_message);
        $this->assertFalse($site->fresh()->hasUnfinishedJob());
        // A job the agent has by now isn't the panel's to end.
        $this->agent->respond('site.deploy', fn () => null);
        $queued = app(StartDeployment::class)->run($site, Deployment::MANUAL);
        $this->assertFalse(app(JobOutcomes::class)->cancelWaiting($queued->agentJob, 'cancelled', 'x'));
        $this->assertSame(Deployment::QUEUED, $queued->fresh()->status);
    }

    /** A job that ended in the panel while the agent was accepting it is stopped on the agent. */
    public function testJobCancelledDuringItsSubmitIsStoppedOnTheAgent(): void
    {
        $site = $this->site();
        $this->connected($site);
        $this->agent->respond('site.deploy', new AgentBusy('site:shop already has a job running'));
        $deployment = app(StartDeployment::class)->run($site, Deployment::WEBHOOK, self::OLDER);
        $job = $deployment->agentJob;
        $this->agent->respond('site.deploy', function () use ($job): ?array {
            app(JobOutcomes::class)->cancelWaiting($job, 'cancelled', 'x');

            return null;
        });

        $this->retry($job, $site, self::OLDER);

        $this->assertSame(AgentJob::CANCELLED, $job->refresh()->status);
        $this->assertNull($job->agent_job_id);
        $this->assertCount(1, array_filter($this->agent->calls(), fn (array $c): bool => $c['command'] === 'job.cancel'));
    }

    /** A deploy may wait behind one whole deploy; other commands keep the 15 minutes. */
    public function testDeployWaitsLongerThanOtherCommands(): void
    {
        $site = $this->site();
        $job = new AgentJob(['command' => 'site.deploy', 'status' => AgentJob::WAITING, 'params' => []]);
        $job->related()->associate($site);
        $job->save();
        $deploy = new SubmitAgentJob($job, new SiteDeploy(site: 'shop', trigger: Deployment::WEBHOOK, composer: false, keepReleases: 5, script: ''));
        $provision = new SubmitAgentJob($job, $site->provisionCommand());

        $this->assertSame(35, SubmitAgentJob::DEPLOY_GIVE_UP_MINUTES);
        $this->assertEquals($job->created_at->copy()->addMinutes(35), $deploy->retryUntil());
        $this->assertEquals($job->created_at->copy()->addMinutes(15), $provision->retryUntil());
        $this->assertStringContainsString('35 minutes', __('zimam.jobs.busy', ['minutes' => $deploy->giveUpMinutes()]));
    }

    /**
     * The agent keeps the result of a deploy that failed after its
     * release went live. The row then names the release that is serving,
     * with the agent's message, which says so.
     */
    public function testDeployThatFailedAfterGoingLiveNamesItsRelease(): void
    {
        $site = $this->site();
        $this->connected($site);
        $deployment = app(StartDeployment::class)->run($site, Deployment::MANUAL, by: $this->user);
        $job = $deployment->agentJob;
        $message = 'release 20261116120000-c0ffee0 is live, but pruning old releases failed: as-user remove-tree: context canceled';

        app(JobOutcomes::class)->apply($job, AgentJobStatus::fromArray([
            'job' => $job->agent_job_id, 'command' => 'site.deploy', 'subject' => 'site:shop', 'status' => AgentJob::FAILED,
            'error_code' => 'internal', 'message' => $message,
            'result' => ['name' => 'shop', 'release' => '20261116120000-c0ffee0', 'commit' => self::NEWER],
        ]));

        $deployment->refresh();
        $this->assertSame(Deployment::FAILED, $deployment->status);
        $this->assertSame('20261116120000-c0ffee0', $deployment->release);
        $this->assertSame(self::NEWER, $deployment->commit);
        $this->assertSame($message, $deployment->error_message);

        // A failure without a result changes neither.
        $plain = app(StartDeployment::class)->run($site, Deployment::WEBHOOK, self::OLDER);
        app(JobOutcomes::class)->apply($plain->agentJob, AgentJobStatus::fromArray([
            'job' => $plain->agentJob->agent_job_id, 'command' => 'site.deploy', 'subject' => 'site:shop', 'status' => AgentJob::FAILED,
            'error_code' => 'internal', 'message' => 'The deploy script failed with exit code 1.', 'result' => null,
        ]));
        $this->assertNull($plain->fresh()->release);
        $this->assertSame(self::OLDER, $plain->fresh()->commit);
    }

    /** No message claims the old state still applies after a cancel: the agent may have finished the change. */
    public function testCancelMessagesDontClaimNothingChanged(): void
    {
        foreach (['zimam.environment.cancelled', 'zimam.deployments.repository.cancelled', 'zimam.deployments.rollback.cancelled'] as $key) {
            $this->assertStringNotContainsString('still applies', __($key), $key);
        }
    }
}
