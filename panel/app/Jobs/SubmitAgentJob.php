<?php

namespace App\Jobs;

use App\Agent\AgentClient;
use App\Agent\AgentCommand;
use App\Agent\Exceptions\AgentBusy;
use App\Agent\Exceptions\AgentException;
use App\Agent\Exceptions\AgentUnavailable;
use App\Models\AgentJob;
use DateTimeInterface;
use Illuminate\Contracts\Queue\ShouldBeEncrypted;
use Illuminate\Contracts\Queue\ShouldQueue;
use Illuminate\Foundation\Queue\Queueable;
use Illuminate\Queue\MaxAttemptsExceededException;
use Throwable;

/**
 * Retries a submit that met `busy`, for up to 15 minutes (D8). The payload
 * holds the full command, secrets included, so the queue stores it encrypted.
 */
final class SubmitAgentJob implements ShouldBeEncrypted, ShouldQueue
{
    use Queueable;

    /** Seconds before each retry: the first follows the busy answer, the last repeats. */
    public const BACKOFF = [5, 10, 20, 40, 60];

    public const GIVE_UP_MINUTES = 15;

    /** An unexpected error fails the job instead of retrying it at once until retryUntil. */
    public int $maxExceptions = 3;

    public function __construct(public AgentJob $agentJob, public AgentCommand $command) {}

    public function handle(AgentClient $agent): void
    {
        try {
            $accepted = $agent->submit($this->command);
        } catch (AgentBusy|AgentUnavailable) {
            $this->release(self::BACKOFF[min($this->attempts(), count(self::BACKOFF) - 1)]);

            return;
        } catch (AgentException $e) {
            // Something changed while it waited, and the agent now refuses it.
            $this->agentJob->markFailed($e->agentCode() ?? 'internal', $e->getMessage());

            return;
        }

        $this->agentJob->markQueued($accepted->id);
        TrackAgentJob::dispatch($this->agentJob);
    }

    public function retryUntil(): DateTimeInterface
    {
        return $this->agentJob->created_at->copy()->addMinutes(self::GIVE_UP_MINUTES);
    }

    /** Running out of time means the subject stayed busy; any other end keeps its own reason. */
    public function failed(?Throwable $e): void
    {
        if ($this->agentJob->refresh()->isFinished()) {
            return;
        }
        if ($e === null || $e instanceof MaxAttemptsExceededException) {
            $this->agentJob->markFailed('busy', __('zimam.jobs.busy'));
        } else {
            $this->agentJob->markFailed('internal', $e->getMessage());
        }
    }
}
