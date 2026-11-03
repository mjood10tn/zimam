<?php

namespace App\Jobs;

use App\Agent\AgentClient;
use App\Agent\Exceptions\AgentException;
use App\Agent\Exceptions\AgentUnavailable;
use App\Agent\JobOutcomes;
use App\Models\AgentJob;
use DateTimeInterface;
use Illuminate\Contracts\Queue\ShouldQueue;
use Illuminate\Foundation\Queue\Queueable;
use Throwable;

/** Polls job.status every 3 seconds until the job ends (§7.4). */
final class TrackAgentJob implements ShouldQueue
{
    use Queueable;

    /** A bug in an outcome handler fails the job instead of retrying it every 3 s until retryUntil. */
    public int $maxExceptions = 3;

    public function __construct(public AgentJob $agentJob) {}

    public function handle(AgentClient $agent, JobOutcomes $outcomes): void
    {
        try {
            $status = $agent->status($this->agentJob->agent_job_id);
        } catch (AgentUnavailable) {
            $this->release(3);

            return;
        } catch (AgentException $e) {
            // not_found: the agent no longer knows the job.
            $outcomes->fail($this->agentJob, $e->agentCode() ?? 'internal', $e->getMessage());

            return;
        }

        if (! $status->isFinished()) {
            $this->agentJob->markRunning($status);
            $this->release(3);

            return;
        }

        // Never follow(): a long job must not hold a queue worker (§7.4).
        $outcomes->apply($this->agentJob, $status);
    }

    /** From when the agent accepted the job, not when it first met `busy`. */
    public function retryUntil(): DateTimeInterface
    {
        return ($this->agentJob->queued_at ?? $this->agentJob->created_at)
            ->copy()
            ->addSeconds($this->agentJob->runtime_max_seconds ?? 0)
            ->addMinutes(10);
    }

    public function backoff(): int
    {
        return 3;
    }

    public function failed(?Throwable $e): void
    {
        if (! $this->agentJob->refresh()->isFinished()) {
            app(JobOutcomes::class)->fail($this->agentJob, 'untracked', __('zimam.jobs.untracked'));
        }
    }
}
