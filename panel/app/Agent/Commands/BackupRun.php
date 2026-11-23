<?php

namespace App\Agent\Commands;

use App\Agent\AgentCommand;
use App\Agent\AgentCommandKind;

/** Runs a plan now. busy while the plan already runs, from its timer too (R05-D9). */
final class BackupRun extends AgentCommand
{
    public function __construct(public readonly string $plan) {}

    public function name(): string
    {
        return 'backup.run';
    }

    public function kind(): AgentCommandKind
    {
        return AgentCommandKind::Job;
    }

    public function params(): array
    {
        return ['plan' => $this->plan];
    }

    public function runtimeMaxSeconds(): ?int
    {
        return 21600;
    }
}
