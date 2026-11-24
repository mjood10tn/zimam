<?php

namespace App\Agent\Commands;

use App\Agent\AgentCommand;
use App\Agent\AgentCommandKind;

/** Lifts one address's ban in one jail. */
final class Fail2banUnban extends AgentCommand
{
    public function __construct(public readonly string $jail, public readonly string $address) {}

    public function name(): string
    {
        return 'fail2ban.unban';
    }

    public function kind(): AgentCommandKind
    {
        return AgentCommandKind::Job;
    }

    public function params(): array
    {
        return ['jail' => $this->jail, 'address' => $this->address];
    }

    public function runtimeMaxSeconds(): ?int
    {
        return 60;
    }
}
