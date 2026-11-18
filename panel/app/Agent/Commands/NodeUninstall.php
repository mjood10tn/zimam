<?php

namespace App\Agent\Commands;

use App\Agent\AgentCommand;
use App\Agent\AgentCommandKind;

/** Removes Node.js; the agent answers busy while a site job runs (R04-D6). */
final class NodeUninstall extends AgentCommand
{
    public function name(): string
    {
        return 'node.uninstall';
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
        return 600;
    }
}
