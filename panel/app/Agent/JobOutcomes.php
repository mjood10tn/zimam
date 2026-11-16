<?php

namespace App\Agent;

use App\Models\AgentJob;
use Closure;
use Illuminate\Support\Facades\DB;

/**
 * What each command's outcome changes in the panel (§7.4). Every outcome
 * updates the agent_jobs row; a handler registered for the command then
 * updates the related records: sites, PHP versions, certificates, findings
 * (Tasks 25–36).
 */
final class JobOutcomes
{
    /** @var array<string, Closure(AgentJob, AgentJobStatus): void> */
    private array $handlers = [];

    /** @param Closure(AgentJob $job, AgentJobStatus $status): void $handler */
    public function register(string $command, Closure $handler): void
    {
        $this->handlers[$command] = $handler;
    }

    public function apply(AgentJob $job, AgentJobStatus $status): void
    {
        DB::transaction(function () use ($job, $status): void {
            $job->markFinished($status);
            $this->handle($job, $status);
        });
    }

    /**
     * The panel gave up on the job (busy for too long, or no longer
     * tracked), or the agent refused it while it waited. It ends failed,
     * and the related records hear so as from a failed outcome, so a site
     * never stays provisioning.
     */
    public function fail(AgentJob $job, string $errorCode, string $message): void
    {
        DB::transaction(function () use ($job, $errorCode, $message): void {
            $job->markFailed($errorCode, $message);
            $this->handle($job, AgentJobStatus::fromArray([
                'job' => (string) $job->agent_job_id,
                'command' => $job->command,
                'subject' => (string) $job->subject,
                'status' => AgentJob::FAILED,
                'error_code' => $errorCode,
                'message' => $message,
            ]));
        });
    }

    /**
     * Ends a job that still waits after `busy`, before the agent has it:
     * the owner's Cancel, or a newer deploy of the same site taking its
     * place. Its queued retry sees the row finished and stops. The related
     * records hear so as from a cancelled outcome. False when the job
     * isn't waiting any more: the agent has it, and only the agent can
     * stop it.
     */
    public function cancelWaiting(AgentJob $job, string $reason, string $message): bool
    {
        return DB::transaction(function () use ($job, $reason, $message): bool {
            if ($job->refresh()->status !== AgentJob::WAITING) {
                return false;
            }
            $job->markCancelled($reason, $message);
            $this->handle($job, AgentJobStatus::fromArray([
                'job' => '',
                'command' => $job->command,
                'subject' => (string) $job->subject,
                'status' => AgentJob::CANCELLED,
                'message' => $message,
            ]));

            return true;
        });
    }

    private function handle(AgentJob $job, AgentJobStatus $status): void
    {
        if (isset($this->handlers[$job->command])) {
            ($this->handlers[$job->command])($job, $status);
        }
    }
}
