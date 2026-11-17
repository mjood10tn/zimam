<?php

namespace App\Agent\Commands;

use App\Agent\AgentCommand;
use App\Agent\AgentCommandKind;

/** Installs Valkey, bound to localhost. The agent runs the install under the server subject; a re-run restarts Valkey. */
final class ValkeyInstall extends AgentCommand
{
    public function name(): string
    {
        return 'valkey.install';
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
        return 900;
    }
}
