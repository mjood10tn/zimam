<?php

namespace App\Agent;

use Closure;

interface AgentClient
{
    /** Read and direct commands. */
    public function call(AgentCommand $command): AgentResult;

    /** Job commands; returns once the agent has accepted the job. */
    public function submit(AgentCommand $command): AgentJob;

    public function status(string $jobId): AgentJobStatus;

    /** Streams log lines from $fromOffset to $onLine until the job ends. */
    public function follow(string $jobId, int $fromOffset, Closure $onLine): AgentJobStatus;
}
