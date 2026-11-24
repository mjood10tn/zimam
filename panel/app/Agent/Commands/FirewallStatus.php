<?php

namespace App\Agent\Commands;

use App\Agent\AgentCommand;
use App\Agent\AgentCommandKind;

/** UFW's rules: Zimam's, the locked ones and the external ones, and whether UFW is on. */
final class FirewallStatus extends AgentCommand
{
    public function name(): string
    {
        return 'firewall.status';
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
