<?php

namespace App\Agent\Commands;

use App\Agent\AgentCommand;
use App\Agent\AgentCommandKind;

/** Removes a site and everything it has on the server (§9.8). */
final class SiteDestroy extends AgentCommand
{
    public function __construct(public readonly string $siteName) {}

    public function name(): string
    {
        return 'site.destroy';
    }

    public function kind(): AgentCommandKind
    {
        return AgentCommandKind::Job;
    }

    public function params(): array
    {
        return ['name' => $this->siteName];
    }

    public function runtimeMaxSeconds(): ?int
    {
        return 300;
    }
}
