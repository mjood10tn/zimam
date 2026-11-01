<?php

namespace Tests\Feature\AgentJob;

use App\Actions\SubmitAgentCommand;
use App\Agent\AgentClient;
use App\Agent\AgentCommand;
use App\Agent\Commands\E2eSleep;
use App\Agent\Exceptions\AgentBusy;
use App\Agent\Exceptions\AgentUnavailable;
use App\Agent\Exceptions\AgentValidationFailed;
use App\Agent\FakeAgentClient;
use App\Jobs\SubmitAgentJob;
use App\Jobs\TrackAgentJob;
use App\Models\AgentJob;
use App\Models\User;
use Filament\Notifications\Notification;
use Filament\Support\Exceptions\Halt;
use Illuminate\Foundation\Testing\RefreshDatabase;
use Illuminate\Support\Carbon;
use Illuminate\Support\Facades\Artisan;
use Illuminate\Support\Facades\DB;
use Illuminate\Support\Facades\Queue;
use RuntimeException;
use Tests\Support\SecretCommand;
use Tests\TestCase;

/** SubmitAgentCommand and the busy retry (D8, design §12). */
final class SubmitTest extends TestCase
{
    use RefreshDatabase;

    private FakeAgentClient $agent;

    private User $admin;

    protected function setUp(): void
    {
        parent::setUp();
        $this->agent = new FakeAgentClient;
        $this->app->instance(AgentClient::class, $this->agent);
        $this->actingAs($this->admin = User::factory()->create());
    }

    public function testSuccessCreatesQueuedJobAndTracks(): void
    {
        Queue::fake();
        $this->freezeSecond();

        $job = $this->submit(new E2eSleep(5, 'blog'), related: $this->admin);

        $job->refresh();
        $this->assertSame(AgentJob::QUEUED, $job->status);
        $this->assertMatchesRegularExpression('/^[0-9A-HJKMNP-TV-Z]{26}\z/', $job->agent_job_id);
        $this->assertSame('e2e.sleep', $job->command);
        $this->assertSame(['seconds' => 5, 'subject' => 'blog'], $job->params);
        $this->assertSame($this->admin->id, $job->created_by);
        $this->assertTrue($job->related->is($this->admin));
        $this->assertSame(180, $job->runtime_max_seconds);
        $this->assertEquals(now(), $job->queued_at);
        Queue::assertPushed(TrackAgentJob::class, fn (TrackAgentJob $track): bool => $track->agentJob->is($job));
        Queue::assertNotPushed(SubmitAgentJob::class);
    }

    public function testValidationErrorsReachCaller(): void
    {
        Queue::fake();

        try {
            $this->submit(new E2eSleep(0, 'blog'));
            $this->fail('no validation error');
        } catch (AgentValidationFailed $e) {
            $this->assertArrayHasKey('seconds', $e->fields());
        }

        $this->assertSame(0, AgentJob::count());
        Queue::assertNothingPushed();
    }

    public function testBusyStoresWaitingAndRetries(): void
    {
        Queue::fake();
        $first = $this->agent->submit(new E2eSleep(5, 'blog'));

        $job = $this->submit(new E2eSleep(7, 'blog'));

        $this->assertSame(AgentJob::WAITING, $job->refresh()->status);
        $this->assertNull($job->agent_job_id);
        $this->assertNull($job->queued_at);
        $retry = null;
        Queue::assertPushed(SubmitAgentJob::class, function (SubmitAgentJob $pushed) use ($job, &$retry): bool {
            $retry = $pushed;

            return $pushed->agentJob->is($job) && $pushed->delay === 5 && $pushed->command->params() === ['seconds' => 7, 'subject' => 'blog'];
        });

        // The first job ends, so the retry is accepted and tracked.
        $this->agent->status($first->id);
        $this->agent->status($first->id);
        $retry->withFakeQueueInteractions()->handle($this->agent);

        $retry->assertNotReleased();
        $this->assertSame(AgentJob::QUEUED, $job->refresh()->status);
        $this->assertNotNull($job->agent_job_id);
        $this->assertNotNull($job->queued_at);
        Queue::assertPushed(TrackAgentJob::class, fn (TrackAgentJob $track): bool => $track->agentJob->is($job));
    }

    /** 5 seconds after the busy answer, then 10, 20, 40 and 60, and 60 from then on. */
    public function testBusyBackoffSequence(): void
    {
        Queue::fake();
        $this->agent->submit(new E2eSleep(5, 'blog'));
        $job = $this->submit(new E2eSleep(7, 'blog'));
        $retry = new SubmitAgentJob($job, new E2eSleep(7, 'blog'));

        foreach ([1 => 10, 2 => 20, 3 => 40, 4 => 60, 5 => 60, 6 => 60] as $attempt => $delay) {
            $retry->withFakeQueueInteractions();
            $retry->job->attempts = $attempt;
            $retry->handle($this->agent);
            $retry->assertReleased($delay);
        }
        $this->assertSame(AgentJob::WAITING, $job->refresh()->status);
    }

    /** Run by a real database queue worker, with the clock moved on to each retry. */
    public function testBusyGivesUpAfter15Minutes(): void
    {
        config(['queue.default' => 'database']);
        $this->freezeSecond();
        $this->agent->scriptJob('e2e.sleep', ['running']);
        $this->agent->submit(new E2eSleep(120, 'blog'));
        $start = now()->toImmutable();

        $job = $this->submit(new E2eSleep(7, 'blog'));
        $attempts = 0;
        while (($next = DB::table('jobs')->min('available_at')) !== null && $attempts < 50) {
            $this->travelTo(Carbon::createFromTimestamp(max(now()->getTimestamp(), (int) $next)));
            Artisan::call('queue:work', ['--once' => true, '--sleep' => 0]);
            $attempts++;
        }

        $job->refresh();
        $this->assertSame(AgentJob::FAILED, $job->status);
        $this->assertSame('busy', $job->error_code);
        $this->assertNotNull($job->finished_at);
        $waited = (int) $start->diffInSeconds(now());
        $this->assertGreaterThan(15 * 60, $waited);
        $this->assertLessThanOrEqual(15 * 60 + 60, $waited);
        $submits = array_filter($this->agent->calls(), fn (array $call): bool => $call['method'] === 'submit');
        // The blocking job, run(), then the retries at 5, 15, 35 and 75 s, and each minute after up to 855 s.
        $this->assertCount(2 + 4 + 13, $submits);
    }

    /** agent_jobs keeps the params with secrets replaced; a waiting retry's payload is encrypted. */
    public function testSecretsNeverStoredInTheClear(): void
    {
        config(['queue.default' => 'database']);
        $job = app(SubmitAgentCommand::class)->run(new SecretCommand);

        $this->assertSame(AgentCommand::REDACTED, $job->refresh()->params['token']);
        $this->assertSame([AgentCommand::REDACTED, AgentCommand::REDACTED], array_column($job->params['users'], 'password_hash'));
        $this->assertSame(300, $job->runtime_max_seconds);

        $this->agent->respond('test.secret', new AgentBusy('the server already has a job running'));
        app(SubmitAgentCommand::class)->run(new SecretCommand);

        $this->assertSame(2, DB::table('jobs')->count());
        $payload = DB::table('jobs')->pluck('payload')->implode("\n");
        $this->assertStringNotContainsString('top-level secret', $payload);
        $this->assertStringNotContainsString('annannann', $payload);
        $this->assertStringNotContainsString('top-level secret', json_encode(AgentJob::all()->toArray()));
    }

    public function testRetryWhileAgentDownKeepsWaiting(): void
    {
        Queue::fake();
        $this->agent->respond('e2e.sleep', new AgentBusy('blog already has a job running'), new AgentUnavailable('connect: connection refused'));
        $job = $this->submit(new E2eSleep(7, 'blog'));

        $retry = (new SubmitAgentJob($job, new E2eSleep(7, 'blog')))->withFakeQueueInteractions();
        $retry->handle($this->agent);

        $retry->assertReleased(10);
        $this->assertSame(AgentJob::WAITING, $job->refresh()->status);
    }

    /** What changed while it waited can make the agent refuse it; that ends the wait. */
    public function testRetryRefusedMarksFailed(): void
    {
        Queue::fake();
        $this->agent->respond('e2e.sleep', new AgentBusy('blog already has a job running'), new AgentValidationFailed('Invalid parameters', ['subject' => 'no such site']));
        $job = $this->submit(new E2eSleep(7, 'blog'));

        $retry = (new SubmitAgentJob($job, new E2eSleep(7, 'blog')))->withFakeQueueInteractions();
        $retry->handle($this->agent);

        $retry->assertNotReleased();
        $this->assertSame(AgentJob::FAILED, $job->refresh()->status);
        $this->assertSame('validation_failed', $job->error_code);
        $this->assertSame('Invalid parameters', $job->error_message);
        Queue::assertNotPushed(TrackAgentJob::class);
    }

    /** Only running out of time is "busy"; a retry that ends any other way keeps its own reason. */
    public function testRetryErrorKeepsItsReason(): void
    {
        Queue::fake();
        $this->agent->respond('e2e.sleep', new AgentBusy('blog already has a job running'));
        $job = $this->submit(new E2eSleep(7, 'blog'));

        (new SubmitAgentJob($job, new E2eSleep(7, 'blog')))->failed(new RuntimeException('the site row is missing'));

        $this->assertSame(AgentJob::FAILED, $job->refresh()->status);
        $this->assertSame('internal', $job->error_code);
        $this->assertSame('the site row is missing', $job->error_message);
    }

    public function testUnavailableNotifies(): void
    {
        Queue::fake();
        $this->agent->respond('e2e.sleep', new AgentUnavailable('connect: no such file'));

        try {
            $this->submit(new E2eSleep(5, 'blog'));
            $this->fail('the action went on');
        } catch (Halt) {
        }

        Notification::assertNotified(__('zimam.agent.unavailable.title'));
        $this->assertSame(0, AgentJob::count());
        Queue::assertNothingPushed();
    }

    private function submit(E2eSleep $command, ?User $related = null): AgentJob
    {
        return app(SubmitAgentCommand::class)->run($command, $related);
    }
}
