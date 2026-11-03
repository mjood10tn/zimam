<?php

namespace App\Agent\Commands;

use App\Agent\AgentCommand;
use App\Agent\AgentCommandKind;

/** Moves a PHP site to another installed version without downtime (§9.5). */
final class SitePhp extends AgentCommand
{
    public function __construct(
        public readonly string $siteName,
        public readonly string $phpVersion,
    ) {}

    public function name(): string
    {
        return 'site.php';
    }

    public function kind(): AgentCommandKind
    {
        return AgentCommandKind::Job;
    }

    public function params(): array
    {
        return ['name' => $this->siteName, 'php_version' => $this->phpVersion];
    }

    public function runtimeMaxSeconds(): ?int
    {
        return 300;
    }
}
