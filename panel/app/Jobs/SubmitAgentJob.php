<?php

namespace App\Jobs;

use App\Agent\AgentClient;
use App\Agent\AgentCommand;
use App\Agent\Commands\JobCancel;
use App\Agent\Commands\SiteDeploy;
use App\Agent\Exceptions\AgentBusy;
use App\Agent\Exceptions\AgentException;
use App\Agent\Exceptions\AgentUnavailable;
use App\Agent\JobOutcomes;
use App\Models\AgentJob;
use DateTimeInterface;
use Illuminate\Contracts\Queue\ShouldBeEncrypted;
use Illuminate\Contracts\Queue\ShouldQueue;
use Illuminate\Foundation\Queue\Queueable;
use Illuminate\Queue\MaxAttemptsExceededException;
use Throwable;

/**
 * Retries a submit that met `busy`, for up to 15 minutes (D8), or 35 for a
 * deploy. The payload holds the full command, secrets included, so the
 * queue stores it encrypted. A job that was cancelled or superseded while
 * it waited is dropped.
 */
final class SubmitAgentJob implements ShouldBeEncrypted, ShouldQueue
{
    use Queueable;

    /** Seconds before each retry: the first follows the busy answer, the last repeats. */
    public const BACKOFF = [5, 10, 20, 40, 60];

    public const GIVE_UP_MINUTES = 15;

    /**
     * A deploy may run 30 minutes (the agent's limit for site.deploy), and
     * the server runs two jobs at a time, so a push can wait behind one
     * whole deploy and still be deployed.
     */
    public const DEPLOY_GIVE_UP_MINUTES = 35;

    /** An unexpected error fails the job instead of retrying it at once until retryUntil. */
    public int $maxExceptions = 3;

    public function __construct(public AgentJob $agentJob, public AgentCommand $command) {}

    public function handle(AgentClient $agent): void
    {
        // Cancelled, or replaced by a newer deploy, while it waited.
        if ($this->agentJob->refresh()->isFinished()) {
            return;
        }
        try {
            $accepted = $agent->submit($this->command);
        } catch (AgentBusy|AgentUnavailable) {
            $this->release(self::BACKOFF[min($this->attempts(), count(self::BACKOFF) - 1)]);

            return;
        } catch (AgentException $e) {
            // Something changed while it waited, and the agent now refuses it.
            app(JobOutcomes::class)->fail($this->agentJob, $e->agentCode() ?? 'internal', $e->getMessage());

            return;
        }

        // It ended in the panel between the check above and the agent's
        // answer: the agent's job would run with nothing following it.
        if ($this->agentJob->refresh()->isFinished()) {
            try {
                $agent->call(new JobCancel($accepted->id));
            } catch (AgentException $e) {
                report($e);
            }

            return;
        }

        $this->agentJob->markQueued($accepted->id);
        TrackAgentJob::dispatch($this->agentJob);
    }

    public function retryUntil(): DateTimeInterface
    {
        return $this->agentJob->created_at->copy()->addMinutes($this->giveUpMinutes());
    }

    public function giveUpMinutes(): int
    {
        return $this->command instanceof SiteDeploy ? self::DEPLOY_GIVE_UP_MINUTES : self::GIVE_UP_MINUTES;
    }

    /** Running out of time means the subject stayed busy; any other end keeps its own reason. */
    public function failed(?Throwable $e): void
    {
        if ($this->agentJob->refresh()->isFinished()) {
            return;
        }
        if ($e === null || $e instanceof MaxAttemptsExceededException) {
            app(JobOutcomes::class)->fail($this->agentJob, 'busy', __('zimam.jobs.busy', ['minutes' => $this->giveUpMinutes()]));
        } else {
            app(JobOutcomes::class)->fail($this->agentJob, 'internal', $e->getMessage());
        }
    }
}
