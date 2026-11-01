<?php

namespace App\Agent\Commands;

use App\Agent\AgentCommand;
use App\Agent\AgentCommandKind;

/** Stops a queued or running job's unit; the job ends cancelled (§5.7). */
final class JobCancel extends AgentCommand
{
    public function __construct(public readonly string $job) {}

    public function name(): string
    {
        return 'job.cancel';
    }

    public function kind(): AgentCommandKind
    {
        return AgentCommandKind::Direct;
    }

    public function params(): array
    {
        return ['job' => $this->job];
    }
}
