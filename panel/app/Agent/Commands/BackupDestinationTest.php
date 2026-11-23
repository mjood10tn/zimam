<?php

namespace App\Agent\Commands;

use App\Agent\AgentCommand;
use App\Agent\AgentCommandKind;

/**
 * Writes, lists and deletes a probe object through the destination's crypt
 * remote. For SFTP the first passed test pins the host key (OQ3).
 */
final class BackupDestinationTest extends AgentCommand
{
    public function __construct(public readonly string $id) {}

    public function name(): string
    {
        return 'backup.destination.test';
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
        return 300;
    }
}
