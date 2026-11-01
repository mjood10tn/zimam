<?php

namespace App\Agent\Commands;

use App\Agent\AgentCommand;
use App\Agent\AgentCommandKind;

/** The server summary the dashboard shows (§7.5). */
final class SystemStatus extends AgentCommand
{
    public function name(): string
    {
        return 'system.status';
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
