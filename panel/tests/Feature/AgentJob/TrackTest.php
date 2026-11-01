<?php

namespace Tests\Feature\AgentJob;

use App\Actions\SubmitAgentCommand;
use App\Agent\AgentClient;
use App\Agent\AgentJobStatus;
use App\Agent\Commands\E2eSleep;
use App\Agent\Exceptions\AgentNotFound;
use App\Agent\Exceptions\AgentUnavailable;
use App\Agent\FakeAgentClient;
use App\Agent\JobOutcomes;
use App\Jobs\TrackAgentJob;
use App\Models\AgentJob;
use App\Models\User;
use Illuminate\Foundation\Testing\RefreshDatabase;
use Illuminate\Queue\MaxAttemptsExceededException;
use Illuminate\Support\Facades\Queue;
use RuntimeException;
use Tests\TestCase;

/** TrackAgentJob polls job.status and never holds a worker (design §7.4). */
final class TrackTest extends TestCase
{
    use RefreshDatabase;

    private FakeAgentClient $agent;

    protected function setUp(): void
    {
        parent::setUp();
        $this->agent = new FakeAgentClient;
        $this->app->instance(AgentClient::class, $this->agent);
        $this->actingAs(User::factory()->create());
        Queue::fake();
    }

    public function testReleasesEvery3SecondsWhileRunning(): void
    {
        $this->agent->scriptJob('e2e.sleep', ['queued', 'running', 'running', 'succeeded']);
        $job = $this->submit();

        $this->track($job)->assertReleased(3);
        $this->assertSame(AgentJob::QUEUED, $job->refresh()->status);
        $this->assertNull($job->started_at);

        $this->track($job)->assertReleased(3);
        $this->assertSame(AgentJob::RUNNING, $job->refresh()->status);
        $this->assertNotNull($job->started_at);

        $this->track($job)->assertReleased(3);
        $this->track($job)->assertNotReleased();
        $this->assertSame(AgentJob::SUCCEEDED, $job->refresh()->status);
    }

    /** Measured from when the agent accepted the job, which may come after minutes of waiting. */
    public function testRetryUntilIsRuntimePlus10Minutes(): void
    {
        $this->freezeSecond();
        $job = $this->submit();
        $job->forceFill(['created_at' => now()->subMinutes(14)])->save();

        $this->assertEquals(now()->addSeconds(180)->addMinutes(10), (new TrackAgentJob($job))->retryUntil());
    }

    public function testNeverCallsFollow(): void
    {
        $this->agent->scriptJob('e2e.sleep', ['running', 'running', 'running', 'succeeded']);
        $job = $this->submit();

        for ($i = 0; $i < 10 && ! $job->refresh()->isFinished(); $i++) {
            $this->track($job);
        }

        $this->assertSame(AgentJob::SUCCEEDED, $job->status);
        $this->assertNotContains('follow', array_column($this->agent->calls(), 'method'));
    }

    public function testAppliesOutcome(): void
    {
        $applied = [];
        app(JobOutcomes::class)->register('e2e.sleep', function (AgentJob $job, AgentJobStatus $status) use (&$applied): void {
            $applied[] = [$job->id, $status->status, $job->status];
        });

        $this->agent->scriptJob('e2e.sleep', ['running', 'succeeded'], result: ['slept' => 5]);
        $ok = $this->submit('blog');
        $this->track($ok);
        $this->track($ok);

        $ok->refresh();
        $this->assertSame(AgentJob::SUCCEEDED, $ok->status);
        $this->assertSame(['slept' => 5], $ok->result);
        $this->assertNotNull($ok->started_at);
        $this->assertNotNull($ok->finished_at);
        $this->assertNull($ok->error_code);

        $this->agent->scriptJob('e2e.sleep', ['running', 'failed'], errorCode: 'internal', message: 'nginx -t failed');
        $failed = $this->submit('shop');
        $this->track($failed);
        $this->track($failed);

        $failed->refresh();
        $this->assertSame(AgentJob::FAILED, $failed->status);
        $this->assertSame('internal', $failed->error_code);
        $this->assertSame('nginx -t failed', $failed->error_message);
        $this->assertSame([[$ok->id, 'succeeded', 'succeeded'], [$failed->id, 'failed', 'failed']], $applied);
    }

    public function testUnavailableReleases(): void
    {
        $job = $this->submit();
        $this->agent->respond('job.status', new AgentUnavailable('connect: connection refused'));

        $this->track($job)->assertReleased(3);
        $this->assertSame(AgentJob::QUEUED, $job->refresh()->status);
    }

    public function testUnknownJobMarksFailed(): void
    {
        $job = $this->submit();
        $this->agent->respond('job.status', new AgentNotFound("No job {$job->agent_job_id}"));

        $this->track($job)->assertNotReleased();

        $this->assertSame(AgentJob::FAILED, $job->refresh()->status);
        $this->assertSame('not_found', $job->error_code);
    }

    /** A failing outcome handler leaves the row unfinished, so the next try applies the whole outcome. */
    public function testFailedHandlerLeavesTheJobUnfinished(): void
    {
        app(JobOutcomes::class)->register('e2e.sleep', fn () => throw new RuntimeException('the site row is missing'));
        $this->agent->scriptJob('e2e.sleep', ['succeeded']);
        $job = $this->submit();

        try {
            $this->track($job);
            $this->fail('the handler error was swallowed');
        } catch (RuntimeException $e) {
            $this->assertSame('the site row is missing', $e->getMessage());
        }

        $this->assertSame(AgentJob::QUEUED, $job->refresh()->status);
        $this->assertNull($job->finished_at);
    }

    /** Past retryUntil the outcome is unknown; the row says so rather than staying "running". */
    public function testGivingUpMarksTheJobUntracked(): void
    {
        $this->agent->scriptJob('e2e.sleep', ['running']);
        $job = $this->submit();
        $this->track($job);

        (new TrackAgentJob($job))->failed(new MaxAttemptsExceededException('too many'));

        $job->refresh();
        $this->assertSame(AgentJob::FAILED, $job->status);
        $this->assertSame('untracked', $job->error_code);
        $this->assertSame(__('zimam.jobs.untracked'), $job->error_message);
        $this->assertNotNull($job->finished_at);
    }

    private function submit(string $subject = 'blog'): AgentJob
    {
        return app(SubmitAgentCommand::class)->run(new E2eSleep(5, $subject));
    }

    private function track(AgentJob $job): TrackAgentJob
    {
        $track = (new TrackAgentJob($job->fresh()))->withFakeQueueInteractions();
        app()->call([$track, 'handle']);

        return $track;
    }
}
