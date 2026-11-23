<?php

namespace App\Agent\Commands;

use App\Agent\AgentCommand;
use App\Agent\AgentCommandKind;

/** A plan's complete backups, read from their manifests on the destination. */
final class BackupList extends AgentCommand
{
    public function __construct(public readonly string $plan) {}

    public function name(): string
    {
        return 'backup.list';
    }

    public function kind(): AgentCommandKind
    {
        return AgentCommandKind::Read;
    }

    public function params(): array
    {
        return ['plan' => $this->plan];
    }
}
