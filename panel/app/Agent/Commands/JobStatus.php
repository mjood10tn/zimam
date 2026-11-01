<?php

namespace App\Agent\Commands;

use App\Agent\AgentCommand;
use App\Agent\AgentCommandKind;

/** A job's state; the agent reconciles it with systemd first (§5.7). */
final class JobStatus extends AgentCommand
{
    public function __construct(public readonly string $job) {}

    public function name(): string
    {
        return 'job.status';
    }

    public function kind(): AgentCommandKind
    {
        return AgentCommandKind::Read;
    }

    public function params(): array
    {
        return ['job' => $this->job];
    }
}
