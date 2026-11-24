<?php

namespace App\Agent\Commands;

use App\Agent\AgentCommand;
use App\Agent\AgentCommandKind;

/** Cancels a scheduled reboot, unattended-upgrades' own included (R06-D14). */
final class SystemRebootCancel extends AgentCommand
{
    public function name(): string
    {
        return 'system.reboot.cancel';
    }

    public function kind(): AgentCommandKind
    {
        return AgentCommandKind::Job;
    }

    public function params(): array
    {
        return [];
    }

    public function runtimeMaxSeconds(): ?int
    {
        return 60;
    }
}
