<?php

namespace App\Agent\Commands;

use App\Agent\AgentCommand;
use App\Agent\AgentCommandKind;

/** Creates a database of a site, named with the site's prefix. */
final class DbCreate extends AgentCommand
{
    public function __construct(public readonly string $site, public readonly string $database) {}

    public function name(): string
    {
        return 'db.create';
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
        return 120;
    }
}
