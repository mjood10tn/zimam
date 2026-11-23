<?php

namespace App\Agent\Commands;

use App\Agent\AgentCommand;
use App\Agent\AgentCommandKind;

/** Removes a plan and its timer. Its backups stay on the destination. */
final class BackupPlanDelete extends AgentCommand
{
    public function __construct(public readonly string $plan) {}

    public function name(): string
    {
        return 'backup.plan.delete';
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
        return 120;
    }
}
