<?php

namespace App\Agent\Commands;

use App\Agent\AgentCommand;
use App\Agent\AgentCommandKind;

/** Drops a database of a site. Its data can't be brought back. */
final class DbDrop extends AgentCommand
{
    public function __construct(public readonly string $site, public readonly string $database) {}

    public function name(): string
    {
        return 'db.drop';
    }

    public function kind(): AgentCommandKind
    {
        return AgentCommandKind::Job;
    }

    public function params(): array
    {
        return ['site' => $this->site, 'database' => $this->database];
    }

    public function runtimeMaxSeconds(): ?int
    {
        return 300;
    }
}
