<?php

namespace App\Agent\Commands;

use App\Agent\AgentCommand;
use App\Agent\AgentCommandKind;

/** Reboots the server now, or at the next HH:MM in its time zone (R06-D14). */
final class SystemReboot extends AgentCommand
{
    public function __construct(public readonly string $at) {}

    public function name(): string
    {
        return 'system.reboot';
    }

    public function kind(): AgentCommandKind
    {
        return AgentCommandKind::Job;
    }

    public function params(): array
    {
        return ['at' => $this->at];
    }

    public function runtimeMaxSeconds(): ?int
    {
        return 60;
    }
}
