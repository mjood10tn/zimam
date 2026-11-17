<?php

namespace App\Agent\Commands;

use App\Agent\AgentCommand;
use App\Agent\AgentCommandKind;

/** Installs MySQL 8.4, bound to localhost. The agent runs the install under the server subject; a re-run restarts MySQL. */
final class MysqlInstall extends AgentCommand
{
    public function name(): string
    {
        return 'mysql.install';
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
