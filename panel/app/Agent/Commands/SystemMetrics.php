<?php

namespace App\Agent\Commands;

use App\Agent\AgentCommand;
use App\Agent\AgentCommandKind;

/** Uptime, load, CPU and network counters, memory, disks and service states (§7.5). */
final class SystemMetrics extends AgentCommand
{
    public function name(): string
    {
        return 'system.metrics';
    }

    public function kind(): AgentCommandKind
    {
        return AgentCommandKind::Read;
    }

    public function params(): array
    {
        return [];
    }
}
