<?php

namespace App\Agent\Commands;

use App\Agent\AgentCommand;
use App\Agent\AgentCommandKind;

/** Makes a PHP version the default: what the php command runs, and what new sites start with (§8.5). */
final class PhpDefault extends AgentCommand
{
    public function __construct(public readonly string $version) {}

    public function name(): string
    {
        return 'php.default';
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
        return 120;
    }
}
