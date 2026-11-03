<?php

namespace App\Agent\Commands;

use App\Agent\AgentCommand;
use App\Agent\AgentCommandKind;

/** Installs a PHP version from the allowlist (§8.3). */
final class PhpInstall extends AgentCommand
{
    public function __construct(public readonly string $version) {}

    public function name(): string
    {
        return 'php.install';
    }

    public function kind(): AgentCommandKind
    {
        return AgentCommandKind::Job;
    }

    public function params(): array
    {
        return ['version' => $this->version];
    }

    public function runtimeMaxSeconds(): ?int
    {
        return 900;
    }
}
