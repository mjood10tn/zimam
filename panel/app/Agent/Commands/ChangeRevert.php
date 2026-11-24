<?php

namespace App\Agent\Commands;

use App\Agent\AgentCommand;
use App\Agent\AgentCommandKind;

/** Undoes a guarded change at once: the owner's Revert now (R06-D2). */
final class ChangeRevert extends AgentCommand
{
    public function __construct(public readonly string $change) {}

    public function name(): string
    {
        return 'change.revert';
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
