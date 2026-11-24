<?php

namespace App\Agent\Commands;

use App\Agent\AgentCommand;
use App\Agent\AgentCommandKind;

/**
 * Turns Ubuntu's automatic security updates on or off (R06-D12) and, with
 * them, an automatic reboot at a set time (R06-D16).
 */
final class UpdatesAuto extends AgentCommand
{
    public function __construct(
        public readonly bool $enabled,
        public readonly bool $autoReboot,
        public readonly ?string $rebootTime = null,
    ) {}

    public function name(): string
    {
        return 'updates.auto';
    }

    public function kind(): AgentCommandKind
    {
        return AgentCommandKind::Job;
    }

    public function params(): array
    {
        return ['enabled' => $this->enabled, 'auto_reboot' => $this->autoReboot]
            + ($this->autoReboot ? ['reboot_time' => (string) $this->rebootTime] : []);
    }

    public function runtimeMaxSeconds(): ?int
    {
        return 600;
    }
}
