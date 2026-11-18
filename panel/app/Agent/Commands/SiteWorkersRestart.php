<?php

namespace App\Agent\Commands;

use App\Agent\AgentCommand;
use App\Agent\AgentCommandKind;

/**
 * Restarts one worker of a site, or all of them without one, gracefully
 * (R04-D4). A worker the agent's state doesn't list is not_found.
 */
final class SiteWorkersRestart extends AgentCommand
{
    public function __construct(
        public readonly string $site,
        public readonly ?string $worker = null,
    ) {}

    public function name(): string
    {
        return 'site.workers.restart';
    }

    public function kind(): AgentCommandKind
    {
        return AgentCommandKind::Job;
    }

    public function params(): array
    {
        return array_filter(['name' => $this->site, 'worker' => $this->worker], fn (?string $v): bool => $v !== null);
    }

    public function runtimeMaxSeconds(): ?int
    {
        return 300;
    }
}
