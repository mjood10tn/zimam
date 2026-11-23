<?php

namespace App\Agent\Commands;

use App\Agent\AgentCommand;
use App\Agent\AgentCommandKind;

/**
 * Run records (R05-D9), one plan's or every plan's, only those whose
 * backup id is after $since when given.
 */
final class BackupRuns extends AgentCommand
{
    public function __construct(
        public readonly ?string $plan = null,
        public readonly ?string $since = null,
    ) {}

    public function name(): string
    {
        return 'backup.runs';
    }

    public function kind(): AgentCommandKind
    {
        return AgentCommandKind::Read;
    }

    public function params(): array
    {
        return array_filter(['plan' => $this->plan, 'since' => $this->since], fn (?string $v): bool => $v !== null);
    }
}
