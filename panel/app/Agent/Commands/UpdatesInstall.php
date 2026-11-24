<?php

namespace App\Agent\Commands;

use App\Agent\AgentCommand;
use App\Agent\AgentCommandKind;

/** Installs pending updates now: Ubuntu's security ones, or all (R06-D13). */
final class UpdatesInstall extends AgentCommand
{
    public function __construct(public readonly string $scope) {}

    public function name(): string
    {
        return 'updates.install';
    }

    public function kind(): AgentCommandKind
    {
        return AgentCommandKind::Job;
    }

    public function params(): array
    {
        return ['scope' => $this->scope];
    }

    public function runtimeMaxSeconds(): ?int
    {
        return 3600;
    }
}
