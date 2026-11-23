<?php

namespace App\Agent\Commands;

use App\Agent\AgentCommand;
use App\Agent\AgentCommandKind;

/**
 * Reads a destination's encryption keys back from rclone.conf, for "Show
 * keys again" (OQ2). A secret-returning read: the agent audits it, and so
 * does agent_actions. The panel shows the keys and keeps none.
 */
final class BackupDestinationKeys extends AgentCommand
{
    public function __construct(public readonly string $id) {}

    public function name(): string
    {
        return 'backup.destination.keys';
    }

    public function kind(): AgentCommandKind
    {
        return AgentCommandKind::Read;
    }

    public function params(): array
    {
        return ['id' => $this->id];
    }

    public function audited(): bool
    {
        return true;
    }
}
