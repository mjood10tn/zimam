<?php

namespace App\Agent\Commands;

use App\Agent\AgentCommand;
use App\Agent\AgentCommandKind;

/** Removes a PHP version nothing uses (§8.4). */
final class PhpUninstall extends AgentCommand
{
    public function __construct(public readonly string $version) {}

    public function name(): string
    {
        return 'php.uninstall';
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
