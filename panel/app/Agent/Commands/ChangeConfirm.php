<?php

namespace App\Agent\Commands;

use App\Agent\AgentCommand;
use App\Agent\AgentCommandKind;

/** Keeps a guarded change: stops its revert timer before the deadline (R06-D2). */
final class ChangeConfirm extends AgentCommand
{
    public function __construct(public readonly string $change) {}

    public function name(): string
    {
        return 'change.confirm';
    }

    public function kind(): AgentCommandKind
    {
        return AgentCommandKind::Direct;
    }

    public function params(): array
    {
        return ['change' => $this->change];
    }
}
