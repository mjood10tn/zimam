<?php

namespace App\Agent\Commands;

use App\Agent\AgentCommand;
use App\Agent\AgentCommandKind;

/** Removes a backup destination no plan uses. Its backups stay where they are. */
final class BackupDestinationDelete extends AgentCommand
{
    public function __construct(public readonly string $id) {}

    public function name(): string
    {
        return 'backup.destination.delete';
    }

    public function kind(): AgentCommandKind
    {
        return AgentCommandKind::Job;
    }

    public function params(): array
    {
        return ['id' => $this->id];
    }

    public function runtimeMaxSeconds(): ?int
    {
        return 120;
    }
}
