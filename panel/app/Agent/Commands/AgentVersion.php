<?php

namespace App\Agent\Commands;

use App\Agent\AgentCommand;
use App\Agent\AgentCommandKind;

/** The agent's version, build and protocols: the handshake, and the health check (D10). */
final class AgentVersion extends AgentCommand
{
    public function name(): string
    {
        return 'agent.version';
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
