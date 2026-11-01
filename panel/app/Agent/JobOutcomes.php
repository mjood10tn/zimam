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
            if (isset($this->handlers[$job->command])) {
                ($this->handlers[$job->command])($job, $status);
            }
        });
    }
}
