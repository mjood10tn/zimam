<?php

namespace App\Agent\Commands;

use App\Agent\AgentCommand;
use App\Agent\AgentCommandKind;

/** A job's log lines from an offset, then its outcome (§5.7). */
final class JobFollow extends AgentCommand
{
    public function __construct(public readonly string $job, public readonly int $fromOffset) {}

    public function name(): string
    {
        return 'job.follow';
    }

    public function kind(): AgentCommandKind
    {
        return AgentCommandKind::Read;
    }

    public function params(): array
    {
        return ['job' => $this->job, 'from_offset' => $this->fromOffset];
    }
}
