<?php

namespace App\Agent\Commands;

use App\Agent\AgentCommand;
use App\Agent\AgentCommandKind;

/**
 * Installs a Node.js major from NodeSource (R04-D6). A different installed
 * major is replaced; the installed major again is the Update (R04-D19).
 */
final class NodeInstall extends AgentCommand
{
    public function __construct(public readonly string $major) {}

    public function name(): string
    {
        return 'node.install';
    }

    public function kind(): AgentCommandKind
    {
        return AgentCommandKind::Job;
    }

    public function params(): array
    {
        return ['major' => $this->major];
    }

    public function runtimeMaxSeconds(): ?int
    {
        return 900;
    }
}
