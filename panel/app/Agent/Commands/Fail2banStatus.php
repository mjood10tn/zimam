<?php

namespace App\Agent\Commands;

use App\Agent\AgentCommand;
use App\Agent\AgentCommandKind;

/** Whether fail2ban is installed and running, and each running jail's bans. */
final class Fail2banStatus extends AgentCommand
{
    public function name(): string
    {
        return 'fail2ban.status';
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
