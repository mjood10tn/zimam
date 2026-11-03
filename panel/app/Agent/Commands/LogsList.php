<?php

namespace App\Agent\Commands;

use App\Agent\AgentCommand;
use App\Agent\AgentCommandKind;

/** A log source's current file and archives (§11.2). */
final class LogsList extends AgentCommand
{
    public function __construct(public readonly string $source, public readonly ?string $site = null) {}

    public function name(): string
    {
        return 'logs.list';
    }

    public function kind(): AgentCommandKind
    {
        return AgentCommandKind::Read;
    }

    public function params(): array
    {
        return array_filter(['source' => $this->source, 'name' => $this->site], fn ($v) => $v !== null);
    }
}
